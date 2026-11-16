//go:build !windows && integration

// Purpose: the composed daemon's completion approval path end to end: a
// Critical job and a degraded Normal review each file one request, the
// elevated tokenless grant records the decision, and the next check
// redeems the token once and accepts; a session principal cannot approve.
//
// Inputs: a composed daemon over a fresh HOME with an injected custody
// whose key is enrolled before start, a real git repository and the real
// approval queue.
//
// SPORT: cmd/cascade completion approval path tests.
package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
)

// approvalPath is a composed daemon with an elevated caller and one job
// at reviewing with complete evidence and a passed acceptance re-run.
type approvalPath struct {
	w    *daemonWiring
	ks   signingKeystore
	call func(ctx context.Context, method string, params, out any) error
}

// newApprovalPath composes the daemon (the hookDaemon composition, with the
// elevation custody enrolled before start) and seeds job-ci at risk.
func newApprovalPath(t *testing.T, risk string, degraded bool) approvalPath {
	t.Helper()
	if exec.Command("git", "--version").Run() != nil {
		t.Skip("git is required for the recorded checkpoint")
	}
	ks := newSigningKeystore(t)
	pub := useElevationCustody(t, ks)
	deps := newRunTestDeps(t, nil)
	deps.SecretsDir, deps.NewKeystore = t.TempDir(), nodes.NewNodeKeystore
	enrollKey(t, deps.Paths.DataDir(), pub)
	executor := &acceptanceExecutor{}
	addTestRegistration(t, daemonRegistration{Name: "test-acceptance-executor", Phase: phaseSupervised, Order: 21,
		Wire: func(w *daemonWiring) error {
			attention, err := newCIAttentionPusher(w.Store, w.Clock, w.Bus, nil)
			if err != nil {
				return err
			}
			rt, err := daemon.NewCISubsystem(w.Ctx, w.Deps.Fleet, w.Deps.Config, w.Bus, attention, w.Clock)
			if err != nil {
				return err
			}
			if executor.inner, err = newLocalTrueExecutor(t, w, rt); err != nil {
				return err
			}
			if err := rt.SetExecutor(executor); err != nil {
				return err
			}
			w.Deps.CI = rt
			return nil
		}})
	seen := observeWiring(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, cleanups, err := composeDaemon(ctx, deps, nil)
	t.Cleanup(func() { cancel(); runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("compose daemon: %v", err)
	}
	w := <-seen
	p := approvalPath{w: w, ks: ks, call: elevatedCaller(w.Registry, ks, runtime.NewSystemClock())}
	p.seed(t, risk, degraded)
	return p
}

// seed records the checkpoint, evidence and passed acceptance for job-ci,
// then sets its risk; degraded makes it a review job on its author's driver.
func (p approvalPath) seed(t *testing.T, risk string, degraded bool) {
	t.Helper()
	ctx, w := context.Background(), p.w
	ref := hookRecordedCheckpoint(t, w, newCIDaemonRig(t))
	hookEvidence(t, w, ref.JobID)
	passed, err := ciAcceptanceRerun(w.Deps.CI.Dispatcher(), ctx, ref, w.Deps.CI.Model(ref.RepoRoot))
	if err != nil || !passed.Passed {
		t.Fatalf("acceptance rerun = %+v, %v", passed, err)
	}
	j := hookStored(t, w, ref.JobID)
	j.RiskClass = risk
	if degraded {
		j.MinTaskClass, j.MutableScope = "review", ""
	}
	if err := w.Deps.Fleet.Jobs().PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if degraded {
		seedReviewerFamilies(t, w, ref.JobID)
		reserveLane(t, w, "author", "author-exec2", "lane-gem-main", 100)
	}
}

func (p approvalPath) check(t *testing.T) hookpacks.CompletionHookResponse {
	t.Helper()
	return hookDispatch(t, p.w, "job-ci")
}

func (p approvalPath) state(t *testing.T) jobs.JobState {
	t.Helper()
	return hookStored(t, p.w, "job-ci").State
}

// pending lists the completion requests the daemon queue holds.
func (p approvalPath) pending(t *testing.T) []policy.PendingEntry {
	t.Helper()
	all, err := p.w.Deps.Policy.Queue.GetPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []policy.PendingEntry
	for _, e := range all {
		if strings.HasPrefix(e.Summary, "accept job ") {
			out = append(out, e)
		}
	}
	return out
}

func (p approvalPath) show(t *testing.T, id string) string {
	t.Helper()
	var out policy.ApprovalShowResult
	if err := p.call(context.Background(), "approval.show", policy.ApprovalShowParams{RequestID: id}, &out); err != nil {
		t.Fatalf("approval.show %s: %v", id, err)
	}
	return out.State
}

// denyingFile runs one check, requires a denial naming one request id, and
// returns that id.
func (p approvalPath) denyingFile(t *testing.T) string {
	t.Helper()
	resp := p.check(t)
	pend := p.pending(t)
	if !resp.Deny || len(pend) != 1 || !strings.Contains(resp.Reason, "completion approval pending: "+pend[0].RequestID) {
		t.Fatalf("first check = %+v, pending %+v; want one denial naming the one request", resp, pend)
	}
	if p.state(t) != jobs.JobStateReviewing {
		t.Fatalf("a denied check moved the job to %s", p.state(t))
	}
	return pend[0].RequestID
}

// grant approves id through the elevated owner path with no token.
func (p approvalPath) grant(t *testing.T, id string) {
	t.Helper()
	if err := p.call(context.Background(), "approval.grant", policy.ApprovalGrantParams{RequestID: id}, nil); err != nil {
		t.Fatalf("approval.grant: %v", err)
	}
}

func TestCriticalCompletionApprovalPath(t *testing.T) {
	p := newApprovalPath(t, string(jobs.RiskClassCritical), false)
	id := p.denyingFile(t)
	if again := p.check(t); !again.Deny || len(p.pending(t)) != 1 {
		t.Fatalf("second check = %+v, pending %d; want a denial and no new request", again, len(p.pending(t)))
	}
	p.grant(t, id)
	if got := p.show(t, id); got != "approved" || p.state(t) != jobs.JobStateReviewing {
		t.Fatalf("after grant: show = %q, job %s; want approved, not consumed, not accepted", got, p.state(t))
	}
	if resp := p.check(t); resp.Deny || p.state(t) != jobs.JobStateAccepted {
		t.Fatalf("check after grant = %+v, job %s; want accepted", resp, p.state(t))
	}
	if got := p.show(t, id); got != "consumed" {
		t.Fatalf("after the redeeming check: show = %q, want consumed", got)
	}
	p.assertAudit(t, id)
}

// assertAudit requires the enqueue, the decision under its effect intent and
// confirm, then the consume row in order, and no row carrying the nonce.
func (p approvalPath) assertAudit(t *testing.T, id string) {
	t.Helper()
	page, err := p.w.Deps.Policy.Audit.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	nonce := p.storedNonce(t)
	var got []string
	for _, r := range page.Records {
		raw, _ := json.Marshal(r)
		if nonce != "" && strings.Contains(string(raw), nonce) {
			t.Fatalf("audit row carries the nonce: %s", raw)
		}
		switch {
		case r.EffectKey == policy.GrantEffectKey(id):
			got = append(got, "effect "+r.EffectPhase)
		case r.EffectKey == "" && r.Kind == audit.KindApprovalEnqueue:
			got = append(got, "enqueue")
		case r.EffectKey == "" && r.Kind == audit.KindApprovalGrant:
			got = append(got, "grant "+r.Outcome)
		}
	}
	want := []string{"enqueue", "effect intent", "grant approved", "effect confirmed", "grant consumed"}
	if !subsequence(got, want) {
		t.Fatalf("audit rows = %q, want %q in order", got, want)
	}
}

// subsequence reports whether want appears in got in order.
func subsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// storedNonce reads the nonce the daemon kept for job-ci's request.
func (p approvalPath) storedNonce(t *testing.T) string {
	t.Helper()
	cp, found, err := p.w.Deps.CI.Dispatcher().CurrentCheckpoint(context.Background(), "job-ci")
	if err != nil || !found {
		t.Fatalf("checkpoint: %v %v", found, err)
	}
	action := jobs.ApprovalScopeAction("job-ci", cp.TreeHash, jobs.CompletionApprovalPolicyVersion)
	row, found, err := jobs.NewStore(p.w.Deps.Fleet.JobsDB()).CompletionApprovalFor(context.Background(), "job-ci", action)
	if err != nil || !found || row.Nonce == "" {
		t.Fatalf("stored completion approval: %+v %v %v", row, found, err)
	}
	_, nonce, _ := strings.Cut(row.Nonce, completionTokenSep)
	return nonce
}

func TestDegradedNormalReviewApproved(t *testing.T) {
	p := newApprovalPath(t, "normal", true)
	id := p.denyingFile(t)
	p.grant(t, id)
	if resp := p.check(t); resp.Deny || p.state(t) != jobs.JobStateAccepted {
		t.Fatalf("check after grant = %+v, job %s; want accepted", resp, p.state(t))
	}
	rows, err := p.w.Deps.Fleet.Jobs().ReviewerDecisions(context.Background(), "job-ci")
	if err != nil || !slices.ContainsFunc(rows, func(d jobs.ReviewerDecision) bool { return d.Outcome == jobs.ReviewOutcomeDegraded }) {
		t.Fatalf("decision rows = %+v, %v; want one with outcome degraded", rows, err)
	}
}

func TestPrincipalCannotApproveCompletion(t *testing.T) {
	p := newApprovalPath(t, string(jobs.RiskClassCritical), false)
	id := p.denyingFile(t)
	d := approvalDaemon{elevatedDaemon: elevatedDaemon{Registry: p.w.Registry, KS: p.ks}}
	if err := d.sessionCall("approval.grant", policy.ApprovalGrantParams{RequestID: id}); err == nil {
		t.Fatal("a session principal's approval.grant was accepted")
	}
	if got := p.show(t, id); got != "pending" {
		t.Fatalf("show = %q after the refused grant, want pending", got)
	}
	if resp := p.check(t); !resp.Deny || p.state(t) != jobs.JobStateReviewing {
		t.Fatalf("check after refused grant = %+v, job %s; want a denial", resp, p.state(t))
	}
}
