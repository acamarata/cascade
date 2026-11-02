// Purpose: checkpoint-bound acceptance and exact commit-only execution tests.
// Where git can run the rig is a real repository; where process exec is
// denied the rig records the same commit-only checkpoint without one.
// SPORT: internal.ci.Dispatcher/AcceptanceRerun (P1-CI-02).
package ci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// gitRuns reports whether this environment may run git at all.
func gitRuns() bool { return exec.Command("git", "--version").Run() == nil }

func acceptanceRig(t *testing.T) (*streamRig, CandidateSnapshot) {
	t.Helper()
	if !gitRuns() {
		return seededAcceptanceRig(t, nil)
	}
	r := newRig(t)
	r.commit(map[string]string{"docs/change.md": "checkpoint\n"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	r.resetRuns()
	return r, snap
}

func (r *streamRig) resetRuns() {
	r.exec.mu.Lock()
	r.exec.runs = nil
	r.exec.mu.Unlock()
}

// seededAcceptanceRig records a checkpoint with the calls Checkpoint ends
// with (openAttempt, then Dispatch) over the real stores, lease manager and
// outbox, and stands in for `git rev-parse <commit>^{tree}` only.
func seededAcceptanceRig(t *testing.T, untracked []string) (*streamRig, CandidateSnapshot) {
	t.Helper()
	redirectHome(t)
	ctx := context.Background()
	r := &streamRig{t: t, repo: t.TempDir(), base: strings.Repeat("1", 40), exec: &recExec{},
		attn: &fakePusher{}, stored: provider.SensitivityInternal}
	r.openRigDBs()
	r.lease = jobs.ResourceLease{RepoID: r.repo, ScopeGlob: "**", Holder: "job-1", Epoch: 1, State: jobs.LeaseHeld}
	if err := r.store.PutLease(ctx, r.lease); err != nil {
		t.Fatal(err)
	}
	r.lm = jobs.NewLeaseManager(r.store, newTestClock(), func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	r.snap = func(context.Context, jobs.FenceFunc, jobs.ResourceLease, int64, []string) (jobs.SnapshotResult, error) {
		return jobs.SnapshotResult{}, errors.New("the seeded rig takes no snapshot")
	}
	r.bus = events.New(storetest.NewMemStore(), newTestClock())
	r.build(r.exec)
	commit, tree := strings.Repeat("2", 40), strings.Repeat("3", 40)
	r.ref = JobRef{JobID: "job-1", ProjectID: "proj", RepoRoot: r.repo, WorktreeRoot: r.repo, Lease: r.lease,
		LeaseEpoch: 1, AttemptID: "att-1", AttemptGeneration: 1, CheckpointCommit: commit, BaseCommit: r.base,
		ScopePrefixes: []string{"docs/"}, PlannedRisk: jobs.RiskClassLow, Sensitivity: provider.SensitivityInternal,
		Untracked: untracked}
	seedCommitTree(t, commit, tree)
	ckID := CheckpointIDFor(r.ref.JobID, commit, tree)
	snap := CandidateSnapshot{TreeHash: tree, CheckpointID: ckID,
		AttemptID: attemptIDFor(r.ref.AttemptID, ckID, r.ref.PlannedRisk), Untracked: untracked}
	row := attemptRow{AttemptID: snap.AttemptID, JobID: "job-1", LeaseID: leaseKey(r.lease), CheckpointID: ckID, TreeHash: tree}
	if err := r.d.openAttempt(ctx, row); err != nil {
		t.Fatal(err)
	}
	plan := CIRequirementPlan{Requirement: AllRequired, Targets: []Target{TargetAll}, Selection: TargetSelectionFull,
		CandidateTreeHash: tree, RiskClass: string(r.ref.PlannedRisk)}
	if _, err := r.d.Dispatch(ctx, r.ref, snap, plan, false); err != nil {
		t.Fatal(err)
	}
	r.resetRuns()
	return r, snap
}

// seedCommitTree makes commit resolve to tree for the rest of the test and
// every other commit unresolvable.
func seedCommitTree(t *testing.T, commit, tree string) {
	saved := acceptanceCommitTree
	acceptanceCommitTree = func(_ context.Context, _, c string) (string, error) {
		if c != commit {
			return "", errors.New("unknown commit " + c)
		}
		return tree, nil
	}
	t.Cleanup(func() { acceptanceCommitTree = saved })
}

func TestAcceptanceBindsCheckpoint(t *testing.T) {
	r, snap := acceptanceRig(t)
	ctx := context.Background()
	before := r.count(r.ciDB, "SELECT COUNT(*) FROM ci_run_stream")
	stale := r.ref
	stale.CheckpointCommit = r.base
	assertAcceptanceRefused(t, r, stale, ErrCheckpointStale.Error(), before)
	if _, err := r.ciDB.Exec("UPDATE ci_stream_attempt SET tree_hash = ?", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	assertAcceptanceRefused(t, r, r.ref, ErrTreeHashMismatch.Error(), before)
	if _, err := r.ciDB.Exec("UPDATE ci_stream_attempt SET tree_hash = ?", snap.TreeHash); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.exec.fn = func(SubJob) (SubJobResult, error) {
		once.Do(func() { close(started) })
		<-release
		return SubJobResult{RunID: -100, RepoID: 7, Passed: true}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := r.d.AcceptanceRerun(ctx, r.ref, RequirementModel{}); done <- err }()
	<-started
	_, concurrentErr := r.d.AcceptanceRerun(ctx, r.ref, RequirementModel{})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errChainHas(concurrentErr, ErrAlreadyRunning) {
		t.Fatalf("concurrent acceptance = %v", concurrentErr)
	}
	assertAcceptanceCached(t, r, snap)
}

func assertAcceptanceRefused(t *testing.T, r *streamRig, ref JobRef, want string, before int) {
	t.Helper()
	_, err := r.d.AcceptanceRerun(context.Background(), ref, RequirementModel{})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("acceptance refusal = %v, want %s", err, want)
	}
	if got := r.count(r.ciDB, "SELECT COUNT(*) FROM ci_run_stream"); got != before {
		t.Fatalf("refusal wrote rows: %d -> %d", before, got)
	}
	if r.exec.count() != 0 {
		t.Fatal("refusal ran the executor")
	}
}

func assertAcceptanceCached(t *testing.T, r *streamRig, snap CandidateSnapshot) {
	t.Helper()
	before, calls := r.count(r.ciDB, "SELECT COUNT(*) FROM ci_run_stream"), r.exec.count()
	first, err := r.d.AcceptanceRerun(context.Background(), r.ref, RequirementModel{})
	if err != nil || !first.Passed || first.CheckpointID != snap.CheckpointID || first.TreeHash != snap.TreeHash {
		t.Fatalf("completed acceptance = %+v, %v", first, err)
	}
	// Reconstruct the dispatcher as well: cached results survive restart.
	r.build(r.exec)
	second, err := r.d.AcceptanceRerun(context.Background(), r.ref, RequirementModel{})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("cached acceptance = %+v, %v; want %+v", second, err, first)
	}
	if got := r.count(r.ciDB, "SELECT COUNT(*) FROM ci_run_stream"); got != before || r.exec.count() != calls {
		t.Fatal("cached acceptance executed again")
	}
}

func TestAcceptanceRerunUsesFullPlanWhenRequired(t *testing.T) {
	for _, name := range []string{"high", "critical", "stale", "uncomputable"} {
		t.Run(name, func(t *testing.T) {
			r, snap := acceptanceRig(t)
			model := RequirementModel{}
			switch name {
			case "high":
				r.ref.PlannedRisk = jobs.RiskClassHigh
			case "critical":
				r.ref.PlannedRisk = jobs.RiskClassCritical
			case "stale":
				r.ref.BaseCommit = strings.Repeat("0", 40)
			case "uncomputable":
				model.Stack, model.Cfg.AffectedCmd = "unknown", "exit 1"
			}
			result, err := r.d.AcceptanceRerun(context.Background(), r.ref, model)
			if err != nil || !result.Passed || result.TreeHash != snap.TreeHash {
				t.Fatalf("acceptance = %+v, %v", result, err)
			}
			want := []string{"architecture", "compile", "format", "integration", "lint", "security", "unit"}
			if got := r.exec.kinds(); !reflect.DeepEqual(got, want) {
				t.Fatalf("kinds = %v, want %v", got, want)
			}
			for _, run := range r.exec.runs {
				if run.Snapshot.TreeHash != snap.TreeHash || run.Plan.Selection != TargetSelectionFull {
					t.Fatalf("run escaped full checkpoint plan: %+v", run)
				}
			}
		})
	}
}

func TestAcceptanceRefusesIndexDivergentTree(t *testing.T) {
	t.Run("stream-only", testAcceptanceStreamOnly)
	t.Run("divergent-tree", func(t *testing.T) {
		r, _ := acceptanceRig(t)
		before := r.count(r.ciDB, "SELECT COUNT(*) FROM ci_run_stream")
		if _, err := r.ciDB.Exec("UPDATE ci_stream_attempt SET tree_hash = ?", strings.Repeat("b", 40)); err != nil {
			t.Fatal(err)
		}
		assertAcceptanceRefused(t, r, r.ref, ErrTreeHashMismatch.Error(), before)
	})
	t.Run("declared-untracked-ignored", testAcceptanceUntrackedIgnored)
}

func testAcceptanceStreamOnly(t *testing.T) {
	var r *streamRig
	if gitRuns() {
		r = newRig(t)
		r.commit(map[string]string{"docs/change.md": "committed\n"})
		writeRepoFile(t, r.wt, "docs/untracked.txt", "untracked\n")
		r.ref.Untracked = []string{"docs/untracked.txt"}
		if _, err := r.checkpoint(); err != nil {
			t.Fatal(err)
		}
		r.resetRuns()
	} else {
		r, _ = seededAcceptanceRig(t, []string{"docs/untracked.txt"})
	}
	before := r.count(r.ciDB, "SELECT COUNT(*) FROM ci_run_stream")
	assertAcceptanceRefused(t, r, r.ref, ErrTreeHashMismatch.Error(), before)
}

func testAcceptanceUntrackedIgnored(t *testing.T) {
	skipWithoutPOSIXShell(t)
	r, snap := acceptanceRig(t)
	r.ref.Untracked = []string{"docs/required.txt"}
	out := filepath.Join(t.TempDir(), "missing.txt")
	if gitRuns() {
		writeRepoFile(t, r.wt, "docs/required.txt", "untracked\n")
		r.build(untrackedShellExecutor(t, r, out))
	} else {
		r.exec.fn = untrackedReader(t, out)
	}
	result, err := r.d.AcceptanceRerun(context.Background(), r.ref, RequirementModel{})
	if err != nil || result.Passed || result.TreeHash != snap.TreeHash {
		t.Fatalf("untracked acceptance = %+v, %v", result, err)
	}
	message, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(message), "docs/required.txt") {
		t.Fatalf("missing path was not named: %s, %v", message, err)
	}
}

// untrackedShellExecutor is the real local executor whose unit kind reads
// the declared untracked path from the materialized commit tree.
func untrackedShellExecutor(t *testing.T, r *streamRig, out string) SubJobExecutor {
	t.Helper()
	commands := map[RequirementKind][]string{}
	for _, kind := range allRequirementKinds {
		commands[kind] = []string{"true"}
	}
	commands[RequirementUnit] = []string{"cat docs/required.txt > '" + out + "' 2>&1"}
	ex, err := NewLocalSubJobExecutor(LocalExecutorDeps{
		CIDB: r.ciDB, Clock: newTestClock(), Exec: ShellExecutor{},
		Commands: commands,
		Environ:  []string{"PATH=" + os.Getenv("PATH")}, RunRoot: quietTemp(t), ModCache: t.TempDir(),
		Populate: func(context.Context, string, []string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// untrackedReader stands in for a unit test that needs the declared path:
// it fails naming the path unless the sub-job was handed it.
func untrackedReader(t *testing.T, out string) func(SubJob) (SubJobResult, error) {
	return func(sj SubJob) (SubJobResult, error) {
		handed := slices.Contains(sj.Ref.Untracked, "docs/required.txt") || slices.Contains(sj.Snapshot.Untracked, "docs/required.txt")
		if sj.Kind != RequirementUnit || handed {
			return SubJobResult{RunID: -1, RepoID: 7, Passed: sj.Kind != RequirementUnit}, nil
		}
		if err := os.WriteFile(out, []byte("docs/required.txt: no such file\n"), 0o600); err != nil {
			t.Error(err)
		}
		return SubJobResult{RunID: -2, RepoID: 7, Passed: false}, nil
	}
}
