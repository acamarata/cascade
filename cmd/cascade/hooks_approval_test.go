//go:build !windows

package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const approvalTestTree = "abcdef0123456789abcdef0123456789abcdef01"

// approvalRig is the daemon's real policy wiring and a jobs db under a
// fresh HOME.
type approvalRig struct {
	pol *policyWiring
	db  *sql.DB
	v   *ciAcceptanceVerifier
}

func newApprovalRig(t *testing.T) approvalRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
	d := newApprovalDaemon(t, nil)
	clock := runtime.NewSystemClock()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := jobs.ApplyGateAttemptsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	w := &daemonWiring{Ctx: context.Background(), Clock: clock, Deps: daemonRuntime{Policy: d.Pol}}
	return approvalRig{pol: d.Pol, db: db, v: &ciAcceptanceVerifier{w: w, jobsDB: db}}
}

func (r approvalRig) approvals(t *testing.T) *completionApprovals {
	t.Helper()
	a, err := completionApprovalsFor(context.Background(), r.v)
	if err != nil || a == nil {
		t.Fatalf("completionApprovalsFor: %v %v", a, err)
	}
	return a
}

func (r approvalRig) pending(t *testing.T) []policy.PendingEntry {
	t.Helper()
	all, err := r.pol.Queue.GetPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var p []policy.PendingEntry // only completion requests; the rig boots others
	for _, e := range all {
		if strings.HasPrefix(e.Summary, "accept job ") {
			p = append(p, e)
		}
	}
	return p
}

func approvalReq(id, tree string) jobs.TransitionRequest {
	j := jobs.Job{ID: id, State: jobs.JobStateReviewing, RiskClass: string(jobs.RiskClassCritical)}
	return jobs.TransitionRequest{Job: &j, Target: jobs.JobStateAccepted, CheckpointID: "cp-" + id, TreeHash: tree,
		PolicyVersion: jobs.CompletionApprovalPolicyVersion}
}

// fileOnce runs one approval denial for req through redeem and afterDenial.
func fileOnce(t *testing.T, a *completionApprovals, req jobs.TransitionRequest) (string, bool) {
	t.Helper()
	st, err := a.redeem(context.Background(), &req)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	reason, wait := a.afterDenial(context.Background(), req, jobs.RiskClassCritical, st, "approval denied")
	return reason, errors.Is(wait, jobs.ErrApprovalPending)
}

func TestComposedGateHasApprovals(t *testing.T) {
	r := newApprovalRig(t)
	a := r.approvals(t)
	if a.approvalQueue() != r.pol.Queue || a.entries != r.pol.Entries {
		t.Fatal("Approvals is not the daemon's one queue (policyWiring.Queue/Entries)")
	}
	g, ok := a.gate(&completionPolicyGate{}).(*approvalCompletionGate)
	if !ok || g.approvals.queue != r.pol.Queue {
		t.Fatalf("composed gate does not carry the daemon queue: %T", a.gate(&completionPolicyGate{}))
	}
	if again := r.approvals(t); again.queue != r.pol.Queue {
		t.Fatal("a second composition built another queue")
	}
	if capRow, err := r.pol.Registry.Lookup(context.Background(), completionCapability); err != nil || capRow.DefaultPolicy != policy.ClassExternalSideEffect {
		t.Fatalf("capability = %+v, %v", capRow, err)
	}
	var none *completionApprovals
	if none.approvalQueue() != nil {
		t.Fatal("no wiring must leave Approvals nil")
	}
	if a, err := completionApprovalsFor(context.Background(), testAcceptanceVerifier{}); a != nil || err != nil {
		t.Fatalf("non-daemon verifier = %v, %v", a, err)
	}
	req := approvalReq("job-a", approvalTestTree)
	reason, pending := fileOnce(t, a, req)
	p := r.pending(t)
	if !pending || len(p) != 1 || reason != approvalPendingReason(p[0].RequestID, "approval denied") {
		t.Fatalf("first check: %q pending=%v entries=%+v", reason, pending, p)
	}
	if !strings.Contains(p[0].Summary, "accept job job-a at tree abcdef012345 (critical)") {
		t.Fatalf("summary = %q", p[0].Summary)
	}
	redeemed := req
	if _, err := a.redeem(context.Background(), &redeemed); err != nil || redeemed.ApprovalRequestID != p[0].RequestID || redeemed.ApprovalNonce == "" {
		t.Fatalf("stored token not redeemed: %q %v", redeemed.ApprovalRequestID, err)
	}
	if strings.Contains(reason, redeemed.ApprovalNonce) {
		t.Fatal("nonce leaked into the reason")
	}
	if reason2, pending2 := fileOnce(t, a, req); !pending2 || reason2 != reason || len(r.pending(t)) != 1 {
		t.Fatalf("second check filed again: %q %v %d", reason2, pending2, len(r.pending(t)))
	}
	if reason3, pending3 := fileOnce(t, a, approvalReq("job-b", "")); pending3 || reason3 != "completion approval needs a checkpoint tree" || len(r.pending(t)) != 1 {
		t.Fatalf("empty tree filed: %q %v", reason3, pending3)
	}
}

// failingRows wraps the real row store with injected errors.
type failingRows struct {
	completionApprovalRows
	readErr, putErr error
}

func (f failingRows) CompletionApprovalFor(ctx context.Context, jobID, action string) (jobs.CompletionApproval, bool, error) {
	if f.readErr != nil {
		return jobs.CompletionApproval{}, false, f.readErr
	}
	return f.completionApprovalRows.CompletionApprovalFor(ctx, jobID, action)
}

func (f failingRows) PutCompletionApproval(ctx context.Context, a jobs.CompletionApproval) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.completionApprovalRows.PutCompletionApproval(ctx, a)
}

// failingQueue is the daemon queue with an injected Enqueue error.
type failingQueue struct {
	policy.ApprovalQueue
	err error
}

func (q failingQueue) Enqueue(context.Context, policy.EnqueueRequest) (policy.EnqueueResult, error) {
	return policy.EnqueueResult{}, q.err
}

// failingEntries is an EntryView source that always errors.
type failingEntries struct{ err error }

func (e failingEntries) EntryView(context.Context, string) (policy.ApprovalEntryView, error) {
	return policy.ApprovalEntryView{}, e.err
}

func TestCompletionApprovalErrorsDeny(t *testing.T) {
	r := newApprovalRig(t)
	ctx := context.Background()
	injected := cascade.New(cascade.KindUnavailable, "injected failure")
	base := r.approvals(t)
	rowsOf := func(a *completionApprovals, id string) bool {
		_, found, err := a.store.CompletionApprovalFor(ctx, id, jobs.ApprovalScopeAction(id, approvalTestTree, jobs.CompletionApprovalPolicyVersion))
		return err == nil && found
	}
	enq := *base
	enq.queue = failingQueue{ApprovalQueue: base.queue, err: injected}
	write := *base
	write.store = failingRows{completionApprovalRows: base.store, putErr: injected}
	for name, a := range map[string]*completionApprovals{"enqueue": &enq, "store write": &write} {
		reason, pending := fileOnce(t, a, approvalReq("job-"+name, approvalTestTree))
		if pending || !strings.Contains(reason, injected.Error()) || len(r.pending(t)) != 0 || rowsOf(base, "job-"+name) {
			t.Fatalf("%s: %q pending=%v entries=%d", name, reason, pending, len(r.pending(t)))
		}
	}
	// Positive leg: the error-free path files one request and stores it.
	if _, pending := fileOnce(t, base, approvalReq("job-ok", approvalTestTree)); !pending || len(r.pending(t)) != 1 || !rowsOf(base, "job-ok") {
		t.Fatal("error-free check did not file one request")
	}
	view := *base
	view.entries = failingEntries{err: injected}
	if reason, pending := fileOnce(t, &view, approvalReq("job-ok", approvalTestTree)); pending || !strings.Contains(reason, injected.Error()) || len(r.pending(t)) != 1 {
		t.Fatalf("entry view: %q pending=%v entries=%d", reason, pending, len(r.pending(t)))
	}
	read := *base
	read.store = failingRows{completionApprovalRows: base.store, readErr: injected}
	assertReadDenies(t, r, &read, injected)
}

// assertReadDenies runs the gate's check with a failing row read: it denies
// with the error in the reason before Transition and files nothing.
func assertReadDenies(t *testing.T, r approvalRig, a *completionApprovals, injected error) {
	t.Helper()
	ctx := context.Background()
	jstore := jobs.NewStore(r.db)
	tier := provider.SensitivityInternal
	job := jobs.Job{ID: "job-read", DataClass: &tier, State: jobs.JobStateReviewing, RiskClass: string(jobs.RiskClassCritical), CreatedAt: 1, UpdatedAt: 1,
		ConsequenceClass: jobs.ConsequenceNormal}
	if err := jstore.PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	cps := &testCompletionCheckpoints{found: true, snap: ci.CandidateSnapshot{CheckpointID: "cp-read", TreeHash: approvalTestTree}}
	before := len(r.pending(t))
	g := a.gate(&completionPolicyGate{store: jstore, checkpoints: cps})
	ok, reason, err := g.CompletionCheck(ctx, job.ID)
	if ok || err != nil || !strings.Contains(reason, injected.Error()) || len(r.pending(t)) != before {
		t.Fatalf("store read: ok=%v %q %v", ok, reason, err)
	}
	if errors.Is(err, injected) {
		t.Fatal("the read error must deny, not fail the check")
	}
}
