//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// hookCheckpoint gives a reviewing job with complete evidence a checkpoint
// in the daemon's dispatcher: through Dispatcher.Checkpoint over a real
// leased repository where git can run, else through the seeded attempt
// (a sandbox that denies process exec).
func hookCheckpoint(t *testing.T, w *daemonWiring) ci.JobRef {
	t.Helper()
	if exec.Command("git", "--version").Run() != nil {
		return hookSeededCheckpoint(t, w, "job-ci")
	}
	ref := hookRecordedCheckpoint(t, w, newCIDaemonRig(t))
	hookEvidence(t, w, ref.JobID)
	return ref
}

// testCompletionCheckpoints records the accepted-edge checkpoint lookup.
type testCompletionCheckpoints struct {
	snap  ci.CandidateSnapshot
	found bool
	err   error
	ids   []string
}

func (c *testCompletionCheckpoints) CurrentCheckpoint(_ context.Context, id string) (ci.CandidateSnapshot, bool, error) {
	c.ids = append(c.ids, id)
	return c.snap, c.found, c.err
}

type testAcceptanceVerifier struct{ verdict jobs.AcceptanceVerdict }

func (v testAcceptanceVerifier) VerifyAcceptance(context.Context, string) (jobs.AcceptanceVerdict, error) {
	return v.verdict, nil
}

// hookEvidence seeds complete evidence through the production producer fence.
func hookEvidence(t *testing.T, w *daemonWiring, id string) (*jobs.EvidenceLedger, jobs.AppendAuthorization) {
	t.Helper()
	ctx, fr := context.Background(), w.Deps.Fleet
	j, found, err := fr.Jobs().GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		tier := provider.SensitivityInternal
		j = jobs.Job{ID: id, CreatedAt: 1, UpdatedAt: 1, MutableScope: "docs/**", ConsequenceClass: jobs.ConsequenceNormal, DataClass: &tier}
	}
	j.State, j.RiskClass = jobs.JobStateReviewing, "normal"
	if err := fr.Jobs().PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := fr.Jobs().PutExecution(ctx, jobs.Execution{ID: id + "-exec", JobID: id, State: jobs.ExecutionRunning}); err != nil {
		t.Fatal(err)
	}
	ls, err := fr.Jobs().LeasesForHolder(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) == 0 {
		a, err := fr.Leases().Acquire(ctx, id+"-repo", "docs/**", id)
		if err != nil || !a.Granted {
			t.Fatalf("lease: %+v %v", a, err)
		}
		ls = []jobs.ResourceLease{a.Lease}
	}
	ledger, _, err := completionLedger(ctx, w.Store, w.Clock, w.Paths, fr.Leases(), completionController(w.Paths))
	if err != nil {
		t.Fatal(err)
	}
	auth := jobs.AppendAuthorization{ExecutionID: id + "-exec", LeaseRepoID: ls[0].RepoID, LeaseScopeGlob: ls[0].ScopeGlob, LeaseEpoch: ls[0].Epoch}
	for _, kind := range []jobs.EvidenceKind{jobs.EvidenceBuild, jobs.EvidenceLint, jobs.EvidenceTests, jobs.EvidenceReview, jobs.EvidenceAdversarial, jobs.EvidenceQA} {
		if _, err := ledger.Append(ctx, hookRecord(id, kind, string(kind)), auth); err != nil {
			t.Fatal(err)
		}
	}
	return ledger, auth
}

func hookRecord(id string, kind jobs.EvidenceKind, key string) jobs.EvidenceRecord {
	return jobs.EvidenceRecord{JobID: id, Kind: kind, ProducerCapability: jobs.ProducerControllerRun,
		Outcome: jobs.OutcomePass, AttestorIdentity: "daemon:test", IdempotencyKey: key}
}

func hookStored(t *testing.T, w *daemonWiring, id string) jobs.Job {
	t.Helper()
	j, found, err := w.Deps.Fleet.Jobs().GetJob(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("stored %s: %v %v", id, found, err)
	}
	return j
}

func TestHooksCompletionPolicyRequiresAcceptance(t *testing.T) {
	executor := &acceptanceExecutor{}
	w := hookDaemon(t, executor)
	hookEvidence(t, w, "complete")
	before := hookStored(t, w, "complete")
	resp := hookDispatch(t, w, "complete")
	if !resp.Deny || !reflect.DeepEqual(before, hookStored(t, w, "complete")) {
		t.Fatalf("no checkpoint allowed: %+v", resp)
	}
	ref := hookCheckpoint(t, w)
	executor.fail.Store(true)
	before = hookStored(t, w, ref.JobID)
	resp = hookDispatch(t, w, ref.JobID)
	if !resp.Deny || !reflect.DeepEqual(before, hookStored(t, w, ref.JobID)) {
		t.Fatalf("failed acceptance advanced job: %+v", resp)
	}
	executor.fail.Store(false)
	passed, err := ciAcceptanceRerun(w.Deps.CI.Dispatcher(), context.Background(), ref, w.Deps.CI.Model(ref.RepoRoot))
	if err != nil || !passed.Passed {
		t.Fatalf("acceptance rerun = %+v %v", passed, err)
	}
	resp = hookDispatch(t, w, ref.JobID)
	if resp.Deny || hookStored(t, w, ref.JobID).State != jobs.JobStateAccepted {
		t.Fatalf("passed acceptance denied: %+v", resp)
	}
}

func TestHooksProducerAuthzFencesStaleEpoch(t *testing.T) {
	w := hookDaemon(t)
	l, auth := hookEvidence(t, w, "fenced")
	before, _ := l.Cursor(context.Background(), "fenced")
	stale := auth
	stale.LeaseEpoch++
	if _, err := l.Append(context.Background(), hookRecord("fenced", jobs.EvidenceBuild, "stale"), stale); err == nil {
		t.Fatal("stale epoch accepted")
	}
	after, _ := l.Cursor(context.Background(), "fenced")
	if before != after || hookStored(t, w, "fenced").State != jobs.JobStateReviewing {
		t.Fatal("denial wrote state")
	}
	if _, err := l.Append(context.Background(), hookRecord("fenced", jobs.EvidenceBuild, "current"), auth); err != nil {
		t.Fatal(err)
	}
}

func TestHooksProducerAuthzRefusesNonController(t *testing.T) {
	w := hookDaemon(t)
	l, auth := hookEvidence(t, w, "role")
	binding := filepath.Join(w.Paths.DataDir(), "nodes", "controller_binding.json")
	if err := os.MkdirAll(filepath.Dir(binding), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binding, []byte(`{"endpoint":"local-controller"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := l.Cursor(context.Background(), "role")
	if _, err := l.Append(context.Background(), hookRecord("role", jobs.EvidenceBuild, "node"), auth); err == nil {
		t.Fatal("node produced evidence")
	}
	after, _ := l.Cursor(context.Background(), "role")
	if before != after || hookStored(t, w, "role").State != jobs.JobStateReviewing {
		t.Fatal("denial wrote state")
	}
	if err := os.Remove(binding); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(context.Background(), hookRecord("role", jobs.EvidenceBuild, "controller"), auth); err != nil {
		t.Fatal(err)
	}
}

func TestHooksProducerAuthzUsesFleetLeaseManager(t *testing.T) {
	w := hookDaemon(t)
	l, _ := hookEvidence(t, w, "identity")
	fencer := reflect.ValueOf(l).Elem().FieldByName("authz").Elem().FieldByName("fencer").Elem()
	if fencer.Pointer() != reflect.ValueOf(w.Deps.Fleet.Leases()).Pointer() {
		t.Fatal("producer fencer differs from fleet lease manager")
	}
}

func TestCompletionCheckTargetsAcceptedWithCheckpoint(t *testing.T) {
	w := hookDaemon(t)
	l, _ := hookEvidence(t, w, "edge")
	cp := &testCompletionCheckpoints{snap: ci.CandidateSnapshot{CheckpointID: "ck", TreeHash: "tree"}, found: true}
	p, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{Store: w.Deps.Fleet.Jobs(), Ledger: l, Clock: w.Clock,
		EngineID: completionGateEngineID, RequireAcceptance: true, Acceptance: testAcceptanceVerifier{jobs.AcceptanceVerdict{Passed: true, CheckpointID: "ck", TreeHash: "tree"}}})
	if err != nil {
		t.Fatal(err)
	}
	g := &completionPolicyGate{policy: p, store: w.Deps.Fleet.Jobs(), checkpoints: cp}
	hookCheckpointDenials(t, w, g, cp)
	cp.found, cp.err, cp.ids = true, nil, nil
	j := hookStored(t, w, "edge")
	j.RiskClass = "normal"
	if err := g.store.PutJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	ok, reason, err := g.CompletionCheck(context.Background(), "edge")
	if err != nil || !ok || hookStored(t, w, "edge").State != jobs.JobStateAccepted || !reflect.DeepEqual(cp.ids, []string{"edge"}) {
		t.Fatalf("accepted: %v %s %v calls=%v", ok, reason, err, cp.ids)
	}
	j.State = jobs.JobStateVerifying
	if err := g.store.PutJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	cp.ids = nil
	ok, reason, err = g.CompletionCheck(context.Background(), "edge")
	if err != nil || !ok || hookStored(t, w, "edge").State != jobs.JobStateReviewing || len(cp.ids) != 0 {
		t.Fatalf("reviewing: %v %s %v calls=%v", ok, reason, err, cp.ids)
	}
	hookNilCheckpoints(t, w)
}

func hookCheckpointDenials(t *testing.T, w *daemonWiring, g *completionPolicyGate, cp *testCompletionCheckpoints) {
	t.Helper()
	for _, failure := range []string{"missing", "error", "risk"} {
		cp.found, cp.err = true, nil
		j := hookStored(t, w, "edge")
		if failure == "missing" {
			cp.found = false
		}
		if failure == "error" {
			cp.err = fmt.Errorf("checkpoint read failed")
		}
		if failure == "risk" {
			j.RiskClass = ""
			if err := g.store.PutJob(context.Background(), j); err != nil {
				t.Fatal(err)
			}
		}
		before := hookStored(t, w, "edge")
		ok, _, err := g.CompletionCheck(context.Background(), "edge")
		if err != nil || ok || !reflect.DeepEqual(before, hookStored(t, w, "edge")) {
			t.Fatalf("%s: %v %v", failure, ok, err)
		}
	}
}

func hookNilCheckpoints(t *testing.T, w *daemonWiring) {
	t.Helper()
	saved := hookpacks.DefaultRegistry
	hookpacks.DefaultRegistry = hookpacks.NewHookRegistry()
	defer func() { hookpacks.DefaultRegistry = saved }()
	r := rpc.NewRegistry()
	err := wireCompletionHookPack(context.Background(), r, w.Store, w.Clock, w.Bus, w.Paths, testAcceptanceVerifier{}, nil, w.Deps.Fleet.Leases(), completionController(w.Paths))
	if !cascade.HasKind(err, cascade.KindInvalidInput) || r.Registered(hookpacks.MethodCompletionCheck) || len(hookpacks.DefaultRegistry.Packs()) != 0 {
		t.Fatalf("nil checkpoints registered: %v", err)
	}
	hookMismatchedDispatcher(t, w, r)
}

func hookMismatchedDispatcher(t *testing.T, w *daemonWiring, r *rpc.Registry) {
	t.Helper()
	v, err := newCIAcceptanceVerifier(w)
	if err != nil {
		t.Fatal(err)
	}
	err = wireCompletionHookPack(context.Background(), r, w.Store, w.Clock, w.Bus, w.Paths,
		v, &testCompletionCheckpoints{}, w.Deps.Fleet.Leases(), completionController(w.Paths))
	if !cascade.HasKind(err, cascade.KindInvalidInput) || r.Registered(hookpacks.MethodCompletionCheck) || len(hookpacks.DefaultRegistry.Packs()) != 0 {
		t.Fatalf("mismatched dispatcher registered: %v", err)
	}
}
