//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type acceptanceExecutor struct {
	run  atomic.Int64
	fail atomic.Bool
	// inner runs every other sub-job over real run rows, which the
	// controller attests; nil keeps the fixture results.
	inner ci.SubJobExecutor
}

func (e *acceptanceExecutor) Run(ctx context.Context, sj ci.SubJob) (ci.SubJobResult, error) {
	if sj.Acceptance && e.fail.Load() {
		return ci.SubJobResult{}, cascade.New(cascade.KindUnavailable, "acceptance executor unavailable")
	}
	if e.inner != nil {
		return e.inner.Run(ctx, sj)
	}
	return ci.SubJobResult{RunID: -e.run.Add(1), RepoID: 17, Passed: true, ExecutorKind: "fixture"}, nil
}

func hookDaemon(t *testing.T, executors ...*acceptanceExecutor) *daemonWiring {
	t.Helper()
	executor := &acceptanceExecutor{}
	if len(executors) > 0 {
		executor = executors[0]
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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
			if exec.Command("git", "--version").Run() == nil {
				if executor.inner, err = newLocalTrueExecutor(t, w, rt); err != nil {
					return err
				}
			}
			if err := rt.SetExecutor(executor); err != nil {
				return err
			}
			w.Deps.CI = rt
			return nil
		}})
	seen := observeWiring(t)
	ctx, cancel := context.WithCancel(context.Background())
	deps := newRunTestDeps(t, nil)
	deps.SecretsDir, deps.NewKeystore = t.TempDir(), nodes.NewNodeKeystore
	_, cleanups, err := composeDaemon(ctx, deps, nil)
	t.Cleanup(func() { cancel(); runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("compose acceptance daemon: %v", err)
	}
	return <-seen
}

func TestHooksCompletionCheckDispatchesDaemonCheckpoint(t *testing.T) {
	hookCheckpointCompletion(t, hookDaemon(t))
}

func hookCheckpointCompletion(t *testing.T, w *daemonWiring) {
	t.Helper()
	v, err := newCIAcceptanceVerifier(w)
	if err != nil || v.dispatcher != w.Deps.CI.Dispatcher() {
		t.Fatalf("acceptance verifier does not hold daemon dispatcher: %v", err)
	}
	hookEvidence(t, w, "no-checkpoint")
	before, _, _ := w.Deps.Fleet.Jobs().GetJob(context.Background(), "no-checkpoint")
	denied := hookDispatch(t, w, "no-checkpoint")
	after, found, err := w.Deps.Fleet.Jobs().GetJob(context.Background(), "no-checkpoint")
	if !denied.Deny || err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("missing checkpoint changed job: response %+v, before %+v after %+v err %v", denied, before, after, err)
	}
	ref := hookCheckpoint(t, w)
	passed, err := ciAcceptanceRerun(w.Deps.CI.Dispatcher(), context.Background(), ref, w.Deps.CI.Model(ref.RepoRoot))
	if err != nil || !passed.Passed {
		t.Fatalf("acceptance rerun = %+v, %v", passed, err)
	}
	resp := hookDispatch(t, w, ref.JobID)
	stored, found, err := w.Deps.Fleet.Jobs().GetJob(context.Background(), ref.JobID)
	if resp.Deny || err != nil || !found || stored.State != jobs.JobStateAccepted {
		t.Fatalf("checkpoint completion = %+v, stored = %+v, %v", resp, stored, err)
	}
}

func hookDispatch(t *testing.T, w *daemonWiring, id string) hookpacks.CompletionHookResponse {
	t.Helper()
	raw, err := json.Marshal(hookpacks.CompletionHookPayload{JobID: id, EventType: hookpacks.EventStop})
	if err != nil {
		t.Fatal(err)
	}
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	got, eo := w.Registry.Dispatch(ctx, &rpc.Request{Method: hookpacks.MethodCompletionCheck, Params: raw})
	if eo != nil {
		t.Fatalf("owner completion.check: %+v", eo)
	}
	return got.(hookpacks.CompletionHookResponse)
}

func hookRecordedCheckpoint(t *testing.T, w *daemonWiring, r *ciDaemonRig) ci.JobRef {
	t.Helper()
	ctx := context.Background()
	if _, err := scope.NewGraphStore(w.Deps.Fleet.JobsDB()).EnsureRepository(ctx, r.repo, "", contextgit.GitCommonDir); err != nil {
		t.Fatal(err)
	}
	lease := r.leaseAndCommit(t, w.Deps.Fleet, "docs", "change.txt")
	wt, found, err := w.Deps.Fleet.Jobs().WorktreeForLease(ctx, lease.RepoID, lease.ScopeGlob)
	if err != nil || !found {
		t.Fatalf("leased worktree: %v", err)
	}
	commit := ciRigGit(t, wt.Path, "rev-parse", "HEAD")
	base := ciRigGit(t, r.repo, "rev-parse", "HEAD")
	job, _, err := w.Deps.Fleet.Jobs().GetJob(ctx, "job-ci")
	if err != nil {
		t.Fatal(err)
	}
	job.State, job.RiskClass = jobs.JobStateReviewing, "normal"
	if err := w.Deps.Fleet.Jobs().PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	ref := ci.JobRef{JobID: job.ID, RepoRoot: r.repo, WorktreeRoot: filepath.Clean(wt.Path),
		Lease: lease, LeaseEpoch: lease.Epoch, CheckpointCommit: commit, BaseCommit: base,
		ScopePrefixes: []string{"docs/"}, PlannedRisk: jobs.RiskClassNormal, Sensitivity: *job.DataClass}
	if _, err := w.Deps.CI.Dispatcher().Checkpoint(ctx, ref, w.Deps.CI.Model(ref.RepoRoot)); err != nil {
		t.Fatalf("daemon dispatcher checkpoint: %v", err)
	}
	return ref
}

// hookSeededCheckpoint records a commit-only checkpoint for id without a
// repository: the attempt row Checkpoint opens, then the daemon dispatcher's
// own Dispatch over the job's real fleet lease. The re-run stand-in below
// replaces only the git commit-tree resolution (internal/ci covers it).
func hookSeededCheckpoint(t *testing.T, w *daemonWiring, id string) ci.JobRef {
	t.Helper()
	ctx, d := context.Background(), w.Deps.CI.Dispatcher()
	hookEvidence(t, w, id)
	ls, err := w.Deps.Fleet.Jobs().LeasesForHolder(ctx, id)
	if err != nil || len(ls) == 0 {
		t.Fatalf("job lease: %v", err)
	}
	commit, tree := strings.Repeat("4", 40), strings.Repeat("5", 40)
	ref := ci.JobRef{JobID: id, RepoRoot: t.TempDir(), Lease: ls[0], LeaseEpoch: ls[0].Epoch, AttemptID: id + "-att",
		CheckpointCommit: commit, BaseCommit: strings.Repeat("6", 40), ScopePrefixes: []string{"docs/"},
		PlannedRisk: jobs.RiskClassNormal, Sensitivity: provider.SensitivityInternal}
	snap := ci.CandidateSnapshot{TreeHash: tree, CheckpointID: ci.CheckpointIDFor(id, commit, tree), AttemptID: id + "-att"}
	if _, err := w.Deps.CI.CIDB().ExecContext(ctx, `INSERT INTO ci_stream_attempt (attempt_id, job_id, lease_id,
		checkpoint_id, tree_hash, state, dispatches, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'live', '[]', 1, 1)`,
		snap.AttemptID, id, ls[0].RepoID+"\x1f"+ls[0].ScopeGlob, snap.CheckpointID, tree); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(ctx, ref, snap, hookFullPlan(tree), false); err != nil {
		t.Fatalf("daemon dispatcher checkpoint: %v", err)
	}
	saved := ciAcceptanceRerun
	ciAcceptanceRerun = func(got *ci.Dispatcher, ctx context.Context, ref ci.JobRef, _ ci.RequirementModel) (ci.AcceptanceResult, error) {
		cur, found, err := got.CurrentCheckpoint(ctx, ref.JobID)
		if got != d || err != nil || !found || cur.CheckpointID != ci.CheckpointIDFor(ref.JobID, ref.CheckpointCommit, tree) {
			return ci.AcceptanceResult{}, ci.ErrCheckpointStale
		}
		results, err := got.Dispatch(ctx, ref, cur, hookFullPlan(cur.TreeHash), true)
		res := ci.AcceptanceResult{CheckpointID: cur.CheckpointID, TreeHash: cur.TreeHash, Passed: err == nil && len(results) > 0, Results: results}
		for _, r := range results {
			res.Passed = res.Passed && r.Passed
		}
		return res, err
	}
	t.Cleanup(func() { ciAcceptanceRerun = saved })
	return ref
}

func hookFullPlan(tree string) ci.CIRequirementPlan {
	return ci.CIRequirementPlan{Requirement: ci.AllRequired, Targets: []ci.Target{ci.TargetAll},
		Selection: ci.TargetSelectionFull, CandidateTreeHash: tree, RiskClass: string(jobs.RiskClassNormal)}
}

func TestAcceptanceIssuesOneNonce(t *testing.T) {
	v, ref, snap := newNonceVerifier(t)
	first, err := v.issue(context.Background(), ref, snap, hookFullPlan(snap.TreeHash))
	if err != nil {
		t.Fatal(err)
	}
	second, err := v.issue(context.Background(), ref, snap, hookFullPlan(snap.TreeHash))
	if err != nil || first != second {
		t.Fatalf("nonce changed: %v %v %v", first, second, err)
	}
	var count int
	if err := v.jobsDB.QueryRow(`SELECT COUNT(*) FROM attestation_nonce`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("issued %d: %v", count, err)
	}
	nonces, _ := jobs.NewAttestationNonceStore(v.jobsDB, v.clock)
	fact, found, err := nonces.Issued(context.Background(), first.value)
	repoID, _ := v.registry.ByPath(context.Background(), ref.RepoRoot)
	job, _, _ := jobs.NewStore(v.jobsDB).GetJob(context.Background(), ref.JobID)
	if err != nil || !found || fact.JobID != ref.JobID || fact.CheckpointID != snap.CheckpointID || fact.RepoID != repoID || fact.JobSpecHash != jobs.JobSpecHash(job, nil) || len(fact.PlanKinds) != 7 {
		t.Fatalf("issued fact %+v: %v", fact, err)
	}
	// A new verifier over the same persisted re-run recovers its nonce.
	v.issued = map[string]acceptanceNonce{}
	third, err := v.issue(context.Background(), ref, snap, hookFullPlan(snap.TreeHash))
	if err != nil || third != first {
		t.Fatalf("recovered nonce %+v: %v", third, err)
	}
}
