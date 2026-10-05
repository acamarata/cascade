// Purpose: the planning half of Checkpoint: changed paths and selection are
// computed from the captured checkpoint tree (never the live HEAD, index or
// worktree), risk only rises, and a changed path outside the lease scope is
// refused at every risk class with nothing dispatched.
//
// SPORT: internal.ci.Dispatcher.Checkpoint/TESTED (P1-CI-01).
package ci

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCheckoutRefusesMissingCommitAndTempDirectory(t *testing.T) {
	redirectHome(t)
	repo := newRigRepo(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	dir, _, err := checkoutCommit(context.Background(), repo, "abcdef0123456789")
	assertStreamError(t, err, cascade.KindUnavailable, "checking out commit")
	if dir != "" {
		t.Fatalf("missing commit returned checkout %q", dir)
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatalf("failed checkout leaked directories: %v, %v", entries, err)
	}
	writeRepoFile(t, tmp, "blocked", "not a directory")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, filepath.Join(tmp, "blocked"))
	}
	_, _, err = checkoutCommit(context.Background(), repo, "HEAD")
	assertStreamError(t, err, cascade.KindUnavailable, "creating the selection checkout directory")
}

func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("the recording affected_cmd is a POSIX shell command")
	}
}

func TestCheckpointChangedPathsFromBaseCommit(t *testing.T) {
	skipWithoutPOSIXShell(t)
	r := newRig(t)
	rec := filepath.Join(t.TempDir(), "affected.rec")
	model := RequirementModel{Stack: "rust", Cfg: Config{AffectedCmd: "cat; { pwd; git rev-parse HEAD; } > '" + rec + "'"}}
	r.commit(map[string]string{"docs/a.md": "a"})
	sha := r.commit(map[string]string{"docs/b.md": "b"})
	writeRepoFile(t, r.wt, "docs/uncommitted.md", "never committed")
	runGit(t, r.wt, "add", "docs/uncommitted.md")

	if _, err := r.d.Checkpoint(context.Background(), r.ref, model); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if len(r.exec.runs) == 0 {
		t.Fatal("nothing dispatched (positive control)")
	}
	plan := r.exec.runs[0].Plan
	if want := []Target{"docs/a.md", "docs/b.md"}; !reflect.DeepEqual(plan.Targets, want) {
		t.Fatalf("plan targets = %v, want exactly the base..checkpoint changed set %v", plan.Targets, want)
	}
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("recording model wrote nothing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[1] != sha {
		t.Fatalf("recording = %q, want the checkout path then the checkpoint commit %s", lines, sha)
	}
	for _, live := range []string{r.wt, r.repo} {
		if lines[0] == live {
			t.Fatalf("selection ran in the live tree %s, want a detached checkout", live)
		}
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("the detached checkout %s must be removed after planning (stat err %v)", lines[0], err)
	}
}

func TestScopeCheckUsesSnapshotTree(t *testing.T) {
	r := newRig(t)
	r.ref.ScopePrefixes = []string{"docs/"}
	captured := r.commit(map[string]string{"outside/x.txt": "x", "docs/a.md": "a"})
	// HEAD advances to a commit that reverts the out-of-scope path.
	runGit(t, r.wt, "rm", "-q", "outside/x.txt")
	runGit(t, r.wt, "commit", "-q", "-m", "revert outside")
	head := gitOutput(t, r.wt, "rev-parse", "HEAD")
	if head == captured {
		t.Fatal("HEAD did not advance")
	}

	r.ref.CheckpointCommit = captured
	_, err := r.checkpoint()
	if !errChainHas(err, ErrScopeViolation) || !strings.Contains(err.Error(), "outside/x.txt") {
		t.Fatalf("captured checkpoint: err = %v, want ErrScopeViolation naming outside/x.txt", err)
	}
	run, outbox, attempt := r.zeroRows()
	if run != 0 || outbox != 0 || attempt != 0 || r.exec.count() != 0 {
		t.Fatalf("a scope violation wrote rows %d/%d/%d or ran %d sub-jobs", run, outbox, attempt, r.exec.count())
	}
	r.ref.CheckpointCommit = head
	if _, err := r.checkpoint(); err != nil {
		t.Fatalf("positive control (the reverted HEAD is in scope): %v", err)
	}
	if r.exec.count() == 0 {
		t.Fatal("positive control dispatched nothing")
	}
}

func TestScopeViolationDenied(t *testing.T) {
	cases := map[string]string{
		"low": "outside/x.md", "normal": "outside/x.go", "high": "pkg/x.go", "critical": "internal/secrets/x.go",
	}
	for class, path := range cases {
		t.Run(class, func(t *testing.T) {
			r := newRig(t)
			r.ref.ScopePrefixes = []string{"docs/"}
			r.commit(map[string]string{path: "x\n"})
			_, err := r.checkpoint()
			if !errChainHas(err, ErrScopeViolation) {
				t.Fatalf("err = %v, want ErrScopeViolation", err)
			}
			if got := len(r.attn.pushed); got != 1 {
				t.Fatalf("attention items = %d, want exactly 1", got)
			}
			if run, outbox, attempt := r.zeroRows(); run+outbox+attempt != 0 || r.exec.count() != 0 {
				t.Fatalf("nothing may be dispatched: rows %d/%d/%d, runs %d", run, outbox, attempt, r.exec.count())
			}
		})
	}
	t.Run("positive control: an in-scope change dispatches", func(t *testing.T) {
		r := newRig(t)
		r.ref.ScopePrefixes = []string{"docs/"}
		r.commit(map[string]string{"docs/a.md": "a"})
		if _, err := r.checkpoint(); err != nil || r.exec.count() == 0 {
			t.Fatalf("in-scope checkpoint: err %v, runs %d", err, r.exec.count())
		}
	})
}

func TestRiskEscalationAtCheckpoint(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	riskOf := func(from int) map[string]bool {
		seen := map[string]bool{}
		for _, sj := range r.exec.runs[from:] {
			seen[sj.Plan.RiskClass] = true
		}
		return seen
	}
	r.commit(map[string]string{"docs/a.md": "a"})
	first, err := r.checkpoint()
	if err != nil {
		t.Fatalf("checkpoint 1: %v", err)
	}
	if got := riskOf(0); !got["low"] || len(got) != 1 || r.exec.count() != 2 {
		t.Fatalf("checkpoint 1 ran %d sub-jobs at %v, want 2 at low", r.exec.count(), got)
	}

	n := r.exec.count()
	r.commit(map[string]string{"pkg/x.go": "package x\n"})
	second, err := r.checkpoint()
	if err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}
	if got := riskOf(n); !got["high"] || len(got) != 1 || r.exec.count()-n != len(allRequirementKinds) {
		t.Fatalf("a rise to high must dispatch the larger set: %d sub-jobs at %v", r.exec.count()-n, got)
	}
	if st := attemptState(t, r, first.AttemptID); st != attemptTombstoned {
		t.Fatalf("the in-flight attempt of the lower class is %q, want tombstoned", st)
	}

	n = r.exec.count()
	r.ref.PlannedRisk = jobs.RiskClassLow // a stale lower plan must not lower the class
	r.commit(map[string]string{"docs/b.md": "b"})
	third, err := r.d.Checkpoint(ctx, r.ref, RequirementModel{})
	if err != nil {
		t.Fatalf("checkpoint 3: %v", err)
	}
	if got := riskOf(n); !got["high"] || len(got) != 1 || r.exec.count()-n != len(allRequirementKinds) {
		t.Fatalf("the class dropped: %d sub-jobs at %v, want %d at high", r.exec.count()-n, got, len(allRequirementKinds))
	}
	if st := attemptState(t, r, second.AttemptID); st != attemptTombstoned {
		t.Fatalf("attempt 2 is %q, want tombstoned", st)
	}
	if third.AttemptID == second.AttemptID {
		t.Fatal("a new checkpoint must open a new attempt")
	}
}

func attemptState(t *testing.T, r *streamRig, id string) string {
	t.Helper()
	var st string
	if err := r.ciDB.QueryRow(`SELECT state FROM ci_stream_attempt WHERE attempt_id = ?`, id).Scan(&st); err != nil {
		t.Fatalf("attempt %s: %v", id, err)
	}
	return st
}

func goModuleFiles(aImportsB bool) map[string]string {
	a := "package a\n\nfunc A() {}\n"
	if aImportsB {
		a = "package a\n\nimport \"example.test/sel/b\"\n\nfunc A() { b.B() }\n"
	}
	return map[string]string{
		"go.mod": "module example.test/sel\n\ngo 1.21\n", "a/a.go": a,
		"b/b.go": "package b\n\nfunc B() {}\n", "c/c.go": "package c\n\nfunc C() {}\n",
	}
}

func TestSelectionIgnoresUncommittedEdits(t *testing.T) {
	r := newRig(t)
	r.ref.ScopePrefixes = []string{"a/", "b/", "c/", "go.mod"}
	model := RequirementModel{Stack: "go"}
	ctx := context.Background()
	r.ref.BaseCommit = r.commit(goModuleFiles(true))
	r.commit(map[string]string{"b/b.go": "package b\n\nfunc B() { _ = 1 }\n"})
	// An uncommitted edit removes a's import of b from the live worktree.
	writeRepoFile(t, r.wt, "a/a.go", goModuleFiles(false)["a/a.go"])

	if _, err := r.d.Checkpoint(ctx, r.ref, model); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	want := []Target{"example.test/sel/a", "example.test/sel/b"}
	if got := r.exec.runs[0].Plan.Targets; !reflect.DeepEqual(got, want) || r.exec.runs[0].Plan.Selection != TargetSelectionAffected {
		t.Fatalf("plan = %v (%s), want the commit's affected set %v", got, r.exec.runs[0].Plan.Selection, want)
	}

	// Positive control: the same edit, committed (before the diff base),
	// narrows the plan to the changed package alone.
	r.ref.BaseCommit = r.commit(nil) // commits the removal of a's import
	r.commit(map[string]string{"b/b.go": "package b\n\nfunc B() { _ = 2 }\n"})
	n := r.exec.count()
	if _, err := r.d.Checkpoint(ctx, r.ref, model); err != nil {
		t.Fatalf("Checkpoint after the commit: %v", err)
	}
	if got := r.exec.runs[n].Plan.Targets; !reflect.DeepEqual(got, []Target{"example.test/sel/b"}) {
		t.Fatalf("committed removal: plan = %v, want only example.test/sel/b", got)
	}
}
