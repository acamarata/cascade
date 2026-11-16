package delegation

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// bridgeFixture: real queue and store, driver double, call log; gate freezes
// the queue; phase is the approval.grant claim the queue's witness shows.
type bridgeFixture struct {
	mu, gate sync.Mutex
	seen     []string
	enq      []policy.EnqueueRequest
	res      []policy.EnqueueResult
	phase    audit.EffectPhase
	logs     bytes.Buffer
	reg      *policy.MemoryRegistry
	clock    *testkit.FrozenClock
	q        loggedQueue
	ch       chan provider.ApprovalRequest
	h        agents.SpawnHandle
	rec      jobs.DelegationRecord
	env      *chain // the record's signed envelope
}

// calls logs add and returns a copy of every call logged so far.
func (f *bridgeFixture) calls(add ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, add...)
	return append([]string(nil), f.seen...)
}

// loggedQueue is the real queue with every call logged in order.
type loggedQueue struct {
	policy.ApprovalQueue
	f *bridgeFixture
}

func (q loggedQueue) GetPending(ctx context.Context) ([]policy.PendingEntry, error) {
	q.f.gate.Lock()
	defer q.f.gate.Unlock()
	return q.ApprovalQueue.GetPending(ctx)
}

func (q loggedQueue) Enqueue(ctx context.Context, req policy.EnqueueRequest) (policy.EnqueueResult, error) {
	res, err := q.ApprovalQueue.Enqueue(ctx, req)
	q.f.mu.Lock()
	q.f.enq, q.f.res = append(q.f.enq, req), append(q.f.res, res)
	q.f.mu.Unlock()
	q.f.calls("enqueue")
	return res, err
}

func (q loggedQueue) ConsumeToken(ctx context.Context, req policy.ConsumeRequest) (policy.ConsumeResult, error) {
	res, err := q.ApprovalQueue.ConsumeToken(ctx, req)
	q.f.calls("consume:" + outcome(err))
	return res, err
}

func (q loggedQueue) Cancel(ctx context.Context, id string) error {
	err := q.ApprovalQueue.Cancel(ctx, id)
	q.f.calls("cancel:" + id + "=" + outcome(err))
	return err
}

// EffectState is the witness: f.phase for a queued entry's claim key only.
func (q loggedQueue) EffectState(_ context.Context, key string) (audit.EffectPhase, bool, error) {
	q.f.mu.Lock()
	defer q.f.mu.Unlock()
	return q.f.phase, q.f.phase != "" && slices.ContainsFunc(q.f.res, func(r policy.EnqueueResult) bool { return policy.GrantEffectKey(r.RequestID) == key }), nil
}

func outcome(err error) string { return map[bool]string{true: "ok", false: "refused"}[err == nil] }

type approvalDriver struct {
	provider.AgentProvider
	f *bridgeFixture
}

func (d approvalDriver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.f.ch }

func (d approvalDriver) ResolveApproval(id string, token string) error {
	d.f.calls("resolve:" + id + "=" + token)
	return nil
}

func newBridgeFixture(t *testing.T) (context.Context, context.CancelFunc, *bridgeFixture) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &bridgeFixture{reg: policy.NewMemoryRegistry(), clock: testkit.NewFrozenClock(time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC))}
	failIf(t, RegisterToolApprovalCapability(ctx, f.reg) != nil, "registering the capability was refused")
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "cascade.db"))
	failIf(t, err != nil, "opening the store: %v", err)
	t.Cleanup(func() { _ = db.Close() })
	grants, err := policy.NewStoreGrants(db, f.reg, f.clock)
	failIf(t, err != nil, "NewStoreGrants: %v", err)
	queue, err := policy.NewApprovalQueue(policy.ApprovalQueueConfig{Store: db, Registry: f.reg, Grants: grants, Clock: f.clock})
	failIf(t, err != nil, "NewApprovalQueue: %v", err)
	f.q, f.ch, f.phase = loggedQueue{ApprovalQueue: queue, f: f}, make(chan provider.ApprovalRequest, 4), audit.EffectConfirmed
	f.h, f.env = agents.SpawnHandle{JobID: "job-1", Driver: approvalDriver{f: f}}, newChain(t)
	f.rec = jobs.DelegationRecord{ID: "deleg-1", AgentPrincipal: "agent-1", Signed: f.env.rootB}
	return ctx, cancel, f
}

func (f *bridgeFixture) start(ctx context.Context, timeout time.Duration) <-chan error {
	out := make(chan error, 1)
	b := approvalBridge{q: f.q, h: f.h, rec: f.rec, timeout: timeout, clock: f.clock, poll: time.Millisecond,
		log: slog.New(slog.NewTextHandler(&f.logs, nil))}
	go func() { out <- b.run(ctx) }()
	return out
}

func (f *bridgeFixture) send(id, prompt string, class provider.DataClass) {
	f.ch <- provider.ApprovalRequest{ID: id, JobID: f.h.JobID, Prompt: prompt, DataClass: class, Kind: provider.ApprovalKindCommand}
}

func (f *bridgeFixture) pending(ctx context.Context, t *testing.T) policy.PendingEntry {
	t.Helper()
	for range 5000 {
		got, err := f.q.GetPending(ctx)
		failIf(t, err != nil || len(got) > 1, "GetPending = %v, %v; want at most one entry", got, err)
		if len(got) == 1 {
			return got[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no queue entry appeared")
	return policy.PendingEntry{}
}

func (f *bridgeFixture) decide(ctx context.Context, t *testing.T, e policy.PendingEntry, approved bool) {
	t.Helper()
	out, err := f.q.Decide(ctx, []policy.DecisionRequest{{RequestID: e.RequestID, Approved: approved, PresentedSummary: e.Summary, PresentedLevel: policy.L2}})
	failIf(t, err != nil || out[0].Err != nil, "Decide: %v %v", err, out)
}

func (f *bridgeFixture) await(t *testing.T, prefix string) string {
	t.Helper()
	for range 5000 {
		for _, c := range f.calls() {
			if rest, ok := strings.CutPrefix(c, prefix); ok {
				return rest
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("calls = %v, want one starting %q", f.calls(), prefix)
	return ""
}

func TestApprovalBridgeEnqueuesWithTier(t *testing.T) {
	ctx, cancel, f := newBridgeFixture(t)
	done := make(chan error, 1)
	go func() { done <- BridgeApprovals(ctx, f.q, f.h, f.rec, time.Hour) }()
	f.send("req-1", "run\n"+strings.Repeat("x", 700), provider.DataClassInternal)
	e := f.pending(ctx, t)
	f.await(t, "enqueue")
	req := f.enq[0]
	failIf(t, req.Capability != CapabilityToolApproval || req.Action != "agent.approval" || req.Level != policy.L2 ||
		req.Subject != policy.Subject{Kind: policy.SubjectAgent, ID: f.rec.AgentPrincipal}, "entry = %q %q %s %+v", req.Capability, req.Action, req.Level, req.Subject)
	failIf(t, len(req.Summary) > 512 || !strings.HasPrefix(req.Summary, "run x") || !strings.HasPrefix(e.Summary, req.Summary), "summary %q (shown %q) is not the truncated prompt", req.Summary, e.Summary)
	var p approvalParams
	err := json.Unmarshal(req.Params, &p)
	failIf(t, err != nil || p.Ref != f.rec.ID || p.Tier != provider.DataClassInternal.String() || p.Request != "req-1",
		"params = %+v (%v), want ref %q tier %q", p, err, f.rec.ID, provider.DataClassInternal)
	cancel()
	failIf(t, f.await(t, "resolve:req-1=") != "", "an undecided request was answered with a token")
	stopped(t, done)
}

func TestApprovalBridgeForwardsOnlyRedeemedToken(t *testing.T) {
	ctx, _, f := newBridgeFixture(t)
	done := f.start(ctx, time.Hour)
	f.send("req-1", "approve me", provider.DataClassInternal)
	f.decide(ctx, t, f.pending(ctx, t), true)
	tok := f.await(t, "resolve:req-1=")
	minted, params := f.res[0].Token, f.enq[0].Params
	failIf(t, tok == "" || tok != minted.Nonce, "driver got %q, want the redeemed token %q", tok, minted.Nonce)
	calls, want := f.calls(), []string{"enqueue", "consume:ok", "resolve:req-1=" + tok}
	failIf(t, !slices.Equal(calls, want), "calls = %v, want %v", calls, want)
	_, err := f.q.ConsumeToken(ctx, policy.ConsumeRequest{RequestID: minted.RequestID, Nonce: minted.Nonce, Action: "agent.approval", Params: params})
	failIf(t, err == nil, "a second redemption of the forwarded token was accepted")
	f.send("req-2", "approve me too", provider.DataClassInternal) // the queue will not redeem this approval
	e := f.pending(ctx, t)
	f.gate.Lock()
	f.decide(ctx, t, e, true)
	err = f.reg.Remove(ctx, CapabilityToolApproval)
	f.gate.Unlock()
	tok = f.await(t, "resolve:req-2=")
	failIf(t, err != nil || tok != "" || !slices.Contains(f.calls(), "consume:refused"),
		"an unredeemed approval forwarded %q (Remove: %v, calls %v)", tok, err, f.calls())
	close(f.ch)
	stopped(t, done)
}

func TestApprovalBridgeDenialCancels(t *testing.T) {
	ctx, cancel, f := newBridgeFixture(t)
	done := f.start(ctx, time.Hour)
	f.send("req-1", "deny me", provider.DataClassPublic)
	e := f.pending(ctx, t)
	f.decide(ctx, t, e, false)
	failIf(t, f.await(t, "resolve:req-1=") != "", "a denial forwarded a token")
	f.await(t, "cancel:"+e.RequestID+"=")
	f.send("req-2", "cancel me", provider.DataClassPublic) // a bridge cancel refuses and withdraws it
	e = f.pending(ctx, t)
	cancel()
	failIf(t, f.await(t, "resolve:req-2=") != "", "a cancel forwarded a token")
	failIf(t, f.await(t, "cancel:"+e.RequestID+"=") != "ok", "the undecided entry was not withdrawn")
	stopped(t, done)
}

func TestApprovalBridgeTimeoutCancels(t *testing.T) {
	ctx, _, f := newBridgeFixture(t)
	done := f.start(ctx, time.Minute)
	f.send("req-1", "wait for me", provider.DataClassRestricted)
	e := f.pending(ctx, t)
	time.Sleep(20 * time.Millisecond)
	failIf(t, len(f.calls()) != 1, "the bridge answered before the injected clock reached the timeout")
	f.clock.Advance(time.Minute)
	failIf(t, f.await(t, "resolve:req-1=") != "", "a timeout forwarded a token")
	got := f.await(t, "cancel:"+e.RequestID+"=")
	failIf(t, got != "ok" || slices.Contains(f.calls(), "consume:ok"), "the timed-out entry was not withdrawn (%s) or was redeemed: %v", got, f.calls())
	close(f.ch)
	stopped(t, done)
}

func TestApprovalBridgeRefusesGrantOutsideEnvelope(t *testing.T) {
	ctx, _, f := newBridgeFixture(t)
	root, host := f.env.root.Env.Roots[0], f.env.root.Env.Egress[0]
	done := f.start(ctx, time.Hour)
	cases := map[string]*provider.PermissionGrant{
		"path": {Paths: []string{root + "/ok", "/etc/passwd"}}, "dots": {Paths: []string{root + "/../../escape"}},
		"rel": {Paths: []string{"sub"}}, "sibling": {Paths: []string{root + "x"}},
		"host": {Hosts: []string{host, "evil.example"}}, "none": nil,
	}
	for id, g := range cases {
		f.ch <- provider.ApprovalRequest{ID: id, JobID: f.h.JobID, Kind: provider.ApprovalKindPermissions, Grant: g}
		failIf(t, f.await(t, "resolve:"+id+"=") != "", "%s: an out-of-envelope grant was answered with a token", id)
	}
	close(f.ch)
	err := <-done
	failIf(t, !isKind(err, cascade.KindPermissionDenied) || strings.Count(err.Error(), "outside the envelope") != len(cases), "the refusals were not reported: %v", err)
	failIf(t, strings.Count(f.logs.String(), "outside the envelope") != len(cases) || len(f.enq) != 0,
		"journal %q and %d entries queued, want %d rows and none", f.logs.String(), len(f.enq), len(cases))
}

func TestApprovalBridgeInEnvelopeGrantEnqueued(t *testing.T) {
	ctx, _, f := newBridgeFixture(t)
	done := f.start(ctx, time.Hour)
	g := &provider.PermissionGrant{Paths: []string{f.env.root.Env.Roots[0] + "/sub/../f"}, Hosts: f.env.root.Env.Egress}
	f.ch <- provider.ApprovalRequest{ID: "req-1", JobID: f.h.JobID, Kind: provider.ApprovalKindPermissions, Grant: g}
	f.decide(ctx, t, f.pending(ctx, t), true)
	failIf(t, f.await(t, "resolve:req-1=") == "" || !slices.Contains(f.calls(), "consume:ok"), "an in-envelope grant was not queued and forwarded: %v", f.calls())
	close(f.ch)
	stopped(t, done)
}

func TestApprovalBridgeUnattestedDecisionForwardsNothing(t *testing.T) {
	ctx, _, f := newBridgeFixture(t)
	done := f.start(ctx, time.Hour)
	for _, phase := range []audit.EffectPhase{"", audit.EffectFailed, audit.EffectUnknown} {
		id := "req-" + string(phase)
		f.phase = phase // the previous request's logged cancel orders this write after its read
		f.send(id, "approve me", provider.DataClassInternal)
		e := f.pending(ctx, t)
		f.decide(ctx, t, e, true)
		tok := f.await(t, "resolve:"+id+"=")
		failIf(t, tok != "" || slices.Contains(f.calls(), "consume:ok"), "claim %q: an unattested decision forwarded %q: %v", phase, tok, f.calls())
		f.await(t, "cancel:"+e.RequestID+"=")
	}
	close(f.ch)
	err := <-done
	failIf(t, err != nil || strings.Count(f.logs.String(), "witnessed approval.grant") != 3,
		"the unattested decisions were not refused and journaled: %v", err)
}

func stopped(t *testing.T, done <-chan error) {
	t.Helper()
	err := <-done
	failIf(t, err != nil, "bridge: %v", err)
}
