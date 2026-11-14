package delegation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// bridgeFixture: a real queue and store, a driver double, and the ordered
// call log. Holding gate keeps the bridge from seeing the queue change.
type bridgeFixture struct {
	mu    sync.Mutex
	calls []string
	enq   []policy.EnqueueRequest
	res   []policy.EnqueueResult
	gate  sync.Mutex
	reg   *policy.MemoryRegistry
	clock *testkit.FrozenClock
	q     loggedQueue
	ch    chan provider.ApprovalRequest
	h     agents.SpawnHandle
	rec   jobs.DelegationRecord
}

func (f *bridgeFixture) add(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *bridgeFixture) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
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
	q.f.add("enqueue")
	return res, err
}

func (q loggedQueue) ConsumeToken(ctx context.Context, req policy.ConsumeRequest) (policy.ConsumeResult, error) {
	res, err := q.ApprovalQueue.ConsumeToken(ctx, req)
	q.f.add("consume:" + outcome(err))
	return res, err
}

func (q loggedQueue) Cancel(ctx context.Context, id string) error {
	err := q.ApprovalQueue.Cancel(ctx, id)
	q.f.add("cancel:" + id + "=" + outcome(err))
	return err
}

func outcome(err error) string { return map[bool]string{true: "ok", false: "refused"}[err == nil] }

// approvalDriver is the driver double: the request channel and logged answers.
type approvalDriver struct {
	provider.AgentProvider
	f *bridgeFixture
}

func (d approvalDriver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.f.ch }

func (d approvalDriver) ResolveApproval(id string, token string) error {
	d.f.add("resolve:" + id + "=" + token)
	return nil
}

func newBridgeFixture(ctx context.Context, t *testing.T) *bridgeFixture {
	t.Helper()
	f := &bridgeFixture{reg: policy.NewMemoryRegistry(), clock: testkit.NewFrozenClock(time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC))}
	if err := RegisterToolApprovalCapability(ctx, f.reg); err != nil {
		t.Fatalf("registering the capability: %v", err)
	}
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	grants, err := policy.NewStoreGrants(db, f.reg, f.clock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	queue, err := policy.NewApprovalQueue(policy.ApprovalQueueConfig{Store: db, Registry: f.reg, Grants: grants, Clock: f.clock})
	if err != nil {
		t.Fatalf("NewApprovalQueue: %v", err)
	}
	f.q, f.ch = loggedQueue{ApprovalQueue: queue, f: f}, make(chan provider.ApprovalRequest, 4)
	f.h = agents.SpawnHandle{JobID: "job-1", Driver: approvalDriver{f: f}}
	f.rec = jobs.DelegationRecord{ID: "deleg-1", AgentPrincipal: "agent-1"}
	return f
}

// start runs the bridge on the fixture's frozen clock with a fast poll.
func (f *bridgeFixture) start(ctx context.Context, timeout time.Duration) <-chan error {
	out := make(chan error, 1)
	b := approvalBridge{q: f.q, h: f.h, rec: f.rec, timeout: timeout, clock: f.clock, poll: time.Millisecond}
	go func() { out <- b.run(ctx) }()
	return out
}

func (f *bridgeFixture) send(id, prompt string, class provider.DataClass) {
	f.ch <- provider.ApprovalRequest{ID: id, JobID: f.h.JobID, Prompt: prompt, DataClass: class, Kind: provider.ApprovalKindCommand}
}

// pending waits until the queue holds exactly one entry and returns it.
func (f *bridgeFixture) pending(ctx context.Context, t *testing.T) policy.PendingEntry {
	t.Helper()
	for range 5000 {
		got, err := f.q.GetPending(ctx)
		if err != nil || len(got) > 1 {
			t.Fatalf("GetPending = %v, %v; want at most one entry", got, err)
		}
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
	if err != nil || out[0].Err != nil {
		t.Fatalf("Decide: %v %v", err, out)
	}
}

// await waits for a logged call starting with prefix and returns the rest.
func (f *bridgeFixture) await(t *testing.T, prefix string) string {
	t.Helper()
	for range 5000 {
		for _, c := range f.snapshot() {
			if rest, ok := strings.CutPrefix(c, prefix); ok {
				return rest
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("calls = %v, want one starting %q", f.snapshot(), prefix)
	return ""
}

func TestApprovalBridgeEnqueuesWithTier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newBridgeFixture(ctx, t)
	done := make(chan error, 1)
	go func() { done <- BridgeApprovals(ctx, f.q, f.h, f.rec, time.Hour) }()
	prompt := "run\n" + strings.Repeat("x", 700)
	f.send("req-1", prompt, provider.DataClassInternal)
	e := f.pending(ctx, t)
	f.mu.Lock()
	req := f.enq[0]
	f.mu.Unlock()
	subject := policy.Subject{Kind: policy.SubjectAgent, ID: f.rec.AgentPrincipal}
	if req.Capability != CapabilityToolApproval || req.Action != "agent.approval" || req.Level != policy.L2 || req.Subject != subject {
		t.Fatalf("entry = %q %q %s %+v", req.Capability, req.Action, req.Level, req.Subject)
	}
	if len(req.Summary) > 512 || !strings.HasPrefix(req.Summary, "run x") || !strings.HasPrefix(e.Summary, req.Summary) {
		t.Fatalf("summary %q (shown %q) is not the truncated prompt", req.Summary, e.Summary)
	}
	var p approvalParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatalf("params: %v", err)
	}
	if p.Ref != f.rec.ID || p.Tier != provider.DataClassInternal.String() || p.Request != "req-1" {
		t.Fatalf("params = %+v, want ref %q tier %q", p, f.rec.ID, provider.DataClassInternal)
	}
	cancel()
	if tok := f.await(t, "resolve:req-1="); tok != "" {
		t.Fatalf("an undecided request was answered with %q", tok)
	}
	stopped(t, done)
}

func TestApprovalBridgeForwardsOnlyRedeemedToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newBridgeFixture(ctx, t)
	done := f.start(ctx, time.Hour)
	f.send("req-1", "approve me", provider.DataClassInternal)
	f.decide(ctx, t, f.pending(ctx, t), true)
	tok := f.await(t, "resolve:req-1=")
	f.mu.Lock()
	minted, params := f.res[0].Token, f.enq[0].Params
	f.mu.Unlock()
	if tok == "" || tok != minted.Nonce {
		t.Fatalf("driver got %q, want the redeemed token %q", tok, minted.Nonce)
	}
	calls := f.snapshot()
	if want := []string{"enqueue", "consume:ok", "resolve:req-1=" + tok}; strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if _, err := f.q.ConsumeToken(ctx, policy.ConsumeRequest{RequestID: minted.RequestID, Nonce: minted.Nonce, Action: "agent.approval", Params: params}); err == nil {
		t.Fatal("a second redemption of the forwarded token was accepted")
	}
	// An approval the queue will not redeem forwards nothing.
	f.send("req-2", "approve me too", provider.DataClassInternal)
	e := f.pending(ctx, t)
	f.gate.Lock()
	f.decide(ctx, t, e, true)
	err := f.reg.Remove(ctx, CapabilityToolApproval)
	f.gate.Unlock()
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if tok := f.await(t, "resolve:req-2="); tok != "" {
		t.Fatalf("an unredeemed approval forwarded %q", tok)
	}
	if !slices.Contains(f.snapshot(), "consume:refused") {
		t.Fatalf("calls = %v, want a refused redemption", f.snapshot())
	}
	close(f.ch)
	stopped(t, done)
}

func TestApprovalBridgeDenialCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newBridgeFixture(ctx, t)
	done := f.start(ctx, time.Hour)
	f.send("req-1", "deny me", provider.DataClassPublic)
	e := f.pending(ctx, t)
	f.decide(ctx, t, e, false)
	if tok := f.await(t, "resolve:req-1="); tok != "" {
		t.Fatalf("a denial forwarded %q", tok)
	}
	f.await(t, "cancel:"+e.RequestID+"=")
	// Cancelling the bridge refuses and withdraws an undecided entry.
	f.send("req-2", "cancel me", provider.DataClassPublic)
	e = f.pending(ctx, t)
	cancel()
	if tok := f.await(t, "resolve:req-2="); tok != "" {
		t.Fatalf("a cancel forwarded %q", tok)
	}
	if got := f.await(t, "cancel:"+e.RequestID+"="); got != "ok" {
		t.Fatalf("the undecided entry was not withdrawn: %s", got)
	}
	stopped(t, done)
}

func TestApprovalBridgeTimeoutCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newBridgeFixture(ctx, t)
	done := f.start(ctx, time.Minute)
	f.send("req-1", "wait for me", provider.DataClassRestricted)
	e := f.pending(ctx, t)
	time.Sleep(20 * time.Millisecond)
	if len(f.snapshot()) != 1 {
		t.Fatal("the bridge answered before the injected clock reached the timeout")
	}
	f.clock.Advance(time.Minute)
	if tok := f.await(t, "resolve:req-1="); tok != "" {
		t.Fatalf("a timeout forwarded %q", tok)
	}
	if got := f.await(t, "cancel:"+e.RequestID+"="); got != "ok" || slices.Contains(f.snapshot(), "consume:ok") {
		t.Fatalf("the timed-out entry was not withdrawn (%s) or was redeemed: %v", got, f.snapshot())
	}
	close(f.ch)
	stopped(t, done)
}

func stopped(t *testing.T, done <-chan error) {
	t.Helper()
	if err := <-done; err != nil {
		t.Fatalf("bridge: %v", err)
	}
}
