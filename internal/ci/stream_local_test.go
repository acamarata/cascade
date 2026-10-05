// Purpose: the snapshot binding and the local executor: a run executes the
// checkpoint COMMIT's tree and nothing else -- not a file edited in the live
// worktree afterwards, not an edit staged after the commit -- and the
// executor is idempotent per (attempt, kind). Real git, real jobs stack.
//
// SPORT: internal.ci.NewLocalSubJobExecutor/TESTED (P1-CI-01).
package ci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestPopulateRefusesUnavailableCacheAndModule(t *testing.T) {
	dir, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	writeRepoFile(t, dir, "go.mod", "module example.test/app\n\ngo 1.26\n\nrequire example.test/m v1.0.0\n")
	env, err := cleanRoomEnv([]string{"PATH=" + os.Getenv("PATH")}, nil, t.TempDir(), cache)
	if err != nil {
		t.Fatal(err)
	}
	l := localExecutor{d: LocalExecutorDeps{ModCache: cache, Environ: env}}
	err = l.populate(context.Background(), dir)
	assertStreamError(t, err, cascade.KindUnavailable, "go download refused the run")
	if !strings.Contains(err.Error(), "GOPROXY=off") {
		t.Fatalf("expected offline module refusal: %v", err)
	}
	blocked := t.TempDir()
	writeRepoFile(t, blocked, "file", "not a directory")
	l.d.ModCache = filepath.Join(blocked, "file", "mod")
	err = l.populate(context.Background(), dir)
	assertStreamError(t, err, cascade.KindUnavailable, "creating the module cache")
}

// recordTree is a lockedExec fn that snapshots, inside the run, which
// paths exist in the run directory and what one file holds.
type treeProbe struct {
	mu       sync.Mutex
	workDirs []string
	files    map[string]string // path relative to WorkDir -> content, "" when absent
}

func (p *treeProbe) fn(paths ...string) func(ExecRequest) ExecResult {
	return func(req ExecRequest) ExecResult {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.workDirs = append(p.workDirs, req.WorkDir)
		for _, rel := range paths {
			raw, err := os.ReadFile(filepath.Join(req.WorkDir, filepath.FromSlash(rel)))
			if err != nil {
				p.files[rel] = "<absent>"
				continue
			}
			p.files[rel] = string(raw)
		}
		return ExecResult{}
	}
}

func TestCandidateSnapshotBindsTree(t *testing.T) {
	r := newRig(t)
	sha := r.commit(map[string]string{"docs/a.md": "a"})
	commitTree := treeOf(t, r.repo, sha)

	plain, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if plain.TreeHash != commitTree || plain.StreamOnly() {
		t.Fatalf("without declared paths the tree must be the commit's %s and not stream-only, got %+v", commitTree, plain)
	}
	if want := CheckpointIDFor("job-1", sha, commitTree); plain.CheckpointID != want {
		t.Fatalf("CheckpointID = %s, want %s", plain.CheckpointID, want)
	}

	r.ref.Untracked = []string{"docs/notes.txt"}
	writeRepoFile(t, r.wt, "docs/notes.txt", "v1")
	v1, err := r.checkpoint()
	if err != nil || v1.TreeHash == commitTree || !v1.StreamOnly() {
		t.Fatalf("declared untracked file must change the tree: %+v, %v", v1, err)
	}
	writeRepoFile(t, r.wt, "docs/notes.txt", "v2 different")
	v2, err := r.checkpoint()
	if err != nil || v2.TreeHash == v1.TreeHash {
		t.Fatalf("editing the declared file must change the tree: %+v, %v", v2, err)
	}
	writeRepoFile(t, r.wt, "docs/undeclared.txt", "never declared")
	v3, err := r.checkpoint()
	if err != nil || v3.TreeHash != v2.TreeHash {
		t.Fatalf("an undeclared untracked file must not change the tree: %+v, %v (want %s)", v3, err, v2.TreeHash)
	}
}

func TestRunTreeIsCommitTree(t *testing.T) {
	r := newRig(t)
	probe := &treeProbe{files: map[string]string{}}
	r.useLocal(&lockedExec{fn: probe.fn("docs/a.md", "staged.txt")}, nil)
	sha := r.commit(map[string]string{"docs/a.md": "committed"})
	// An out-of-band file is staged after the checkpoint commit.
	writeRepoFile(t, r.wt, "staged.txt", "staged after the commit")
	runGit(t, r.wt, "add", "staged.txt")
	indexTree := gitOutput(t, r.wt, "write-tree")
	commitTree := treeOf(t, r.repo, sha)
	if indexTree == commitTree {
		t.Fatal("positive control: the index tree must differ from the commit tree")
	}

	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if snap.TreeHash != commitTree || snap.TreeHash == indexTree {
		t.Fatalf("TreeHash = %s, want the commit tree %s (index tree %s)", snap.TreeHash, commitTree, indexTree)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.files["docs/a.md"] != "committed" {
		t.Fatalf("run dir docs/a.md = %q, want the committed content (positive control)", probe.files["docs/a.md"])
	}
	if probe.files["staged.txt"] != "<absent>" {
		t.Fatalf("run dir staged.txt = %q, want absent: the run executes the commit tree only", probe.files["staged.txt"])
	}
}

func TestLocalExecutorRunsSnapshotTree(t *testing.T) {
	r := newRig(t)
	probe := &treeProbe{files: map[string]string{}}
	r.useLocal(&lockedExec{fn: probe.fn("docs/a.md")}, nil)
	r.commit(map[string]string{"docs/a.md": "as committed"})
	if _, err := r.checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	// A file edited in the live worktree after the snapshot is absent from a
	// later run of the same snapshot: the run materializes the tree object.
	writeRepoFile(t, r.wt, "docs/a.md", "edited live after the snapshot")
	probe.mu.Lock()
	first := probe.files["docs/a.md"]
	probe.files = map[string]string{}
	probe.mu.Unlock()
	if first != "as committed" {
		t.Fatalf("first run saw %q, want the committed content", first)
	}

	ex := r.d.deps.Executor
	snap, _, _ := r.d.CurrentCheckpoint(context.Background(), "job-1")
	sj := SubJob{Ref: r.ref, Kind: RequirementUnit, Snapshot: CandidateSnapshot{TreeHash: snap.TreeHash, AttemptID: "late-run"},
		Plan: CIRequirementPlan{Selection: TargetSelectionFull}}
	if _, err := ex.Run(context.Background(), sj); err != nil {
		t.Fatalf("second run of the same tree: %v", err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.files["docs/a.md"] != "as committed" {
		t.Fatalf("second run saw %q, want the committed content, not the live edit", probe.files["docs/a.md"])
	}
	for _, wd := range probe.workDirs {
		if wd == r.wt || wd == r.repo || strings.HasPrefix(wd, r.wt) {
			t.Fatalf("a run executed in the live tree %s", wd)
		}
	}
}

func TestLocalExecutorIsIdempotentPerAttemptAndKind(t *testing.T) {
	r := newRig(t)
	fx := &lockedExec{}
	r.useLocal(fx, nil)
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	ex := r.d.deps.Executor
	sj := SubJob{Ref: r.ref, Kind: RequirementFormat, Snapshot: snap, Plan: CIRequirementPlan{Selection: TargetSelectionFull}}
	calls := len(fx.commands())
	again, err := ex.Run(context.Background(), sj)
	if err != nil || !again.Passed || len(fx.commands()) != calls {
		t.Fatalf("re-running a completed sub-job = %+v, %v; ran %d new commands, want 0", again, err, len(fx.commands())-calls)
	}

	// An interrupted run left a reserved placeholder: the next run takes it.
	sj.Kind = RequirementSecurity
	repoID, name := LocalRepoID(r.repo), streamRunName(sj)
	reserved, err := ReserveLocalRun(context.Background(), r.ciDB, repoID, name, newTestClock().Now())
	if err != nil {
		t.Fatalf("ReserveLocalRun: %v", err)
	}
	res, err := ex.Run(context.Background(), sj)
	if err != nil || res.RunID != reserved {
		t.Fatalf("Run = %+v, %v; want it to complete the reserved run %d", res, err, reserved)
	}
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_run WHERE name = ?`, name); n != 1 {
		t.Fatalf("%d ci_run rows for the interrupted sub-job, want exactly 1", n)
	}
}

func TestLocalExecutorRefusesADeclaredUntrackedPathForAcceptance(t *testing.T) {
	r := newRig(t)
	fx := &lockedExec{}
	r.useLocal(fx, nil)
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	ref := r.ref
	ref.Untracked = []string{"docs/needed-but-untracked.txt"}
	sj := SubJob{Ref: ref, Kind: RequirementUnit, Acceptance: true, Snapshot: CandidateSnapshot{TreeHash: snap.TreeHash, AttemptID: "acc"},
		Plan: CIRequirementPlan{Selection: TargetSelectionFull}}
	calls := len(fx.commands())
	_, err = r.d.deps.Executor.Run(context.Background(), sj)
	if !errChainHas(err, ErrTreeHashMismatch) || !strings.Contains(err.Error(), "docs/needed-but-untracked.txt") {
		t.Fatalf("err = %v, want ErrTreeHashMismatch naming the path", err)
	}
	if len(fx.commands()) != calls || r.count(r.ciDB, `SELECT COUNT(*) FROM ci_run WHERE name = ?`, streamRunName(sj)) != 0 {
		t.Fatal("a refused acceptance run executed commands or reserved a run")
	}
}

func TestRunCompletedViaStreamFlag(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	r.commit(map[string]string{"docs/a.md": "a"})
	if _, err := r.checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	streamed := completedPayloads(t, r.bus)
	if len(streamed) != 2 {
		t.Fatalf("stream ci.run.completed events = %d, want 2", len(streamed))
	}
	for _, p := range streamed {
		if !p.ViaStream {
			t.Fatalf("stream run payload = %+v, want via_stream true", p)
		}
	}

	deps := newTestDeps(t, &fakeExecutor{})
	cfg := stepsInOrder(map[StepKind]string{StepLint: "lint-cmd"})
	if _, err := Execute(context.Background(), deps, cfg, 1, 2, "plain"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	plain := completedPayloads(t, deps.Events)
	if len(plain) != 1 || plain[0].ViaStream {
		t.Fatalf("`cascade ci run` payloads = %+v, want one with via_stream false", plain)
	}
	all, _ := deps.Events.Replay(context.Background(), EventNamespace, 0)
	if !strings.Contains(string(all[0].Payload), `"via_stream":false`) {
		t.Fatalf("raw payload %s must carry the explicit via_stream field", all[0].Payload)
	}
}

func completedPayloads(t *testing.T, bus *events.Bus) []runCompletedPayload {
	t.Helper()
	all, err := bus.Replay(context.Background(), EventNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var out []runCompletedPayload
	for _, ev := range all {
		if ev.Kind != EventKindRunCompleted {
			continue
		}
		var p runCompletedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func TestTargetArgsExpansion(t *testing.T) {
	if got, err := targetArgs(CIRequirementPlan{Selection: TargetSelectionFull, Targets: []Target{TargetAll}}); err != nil || got != "./..." {
		t.Fatalf("full plan = %q, %v; want ./...", got, err)
	}
	affected := CIRequirementPlan{Selection: TargetSelectionAffected, Targets: []Target{"example.test/a", "example.test/b"}}
	if got, err := targetArgs(affected); err != nil || got != "example.test/a example.test/b" {
		t.Fatalf("affected plan = %q, %v", got, err)
	}
	for _, bad := range []Target{"a; rm -rf /", "$(id)", "a b", TargetAll} {
		unsafe := CIRequirementPlan{Selection: TargetSelectionAffected, Targets: []Target{bad}}
		if _, err := targetArgs(unsafe); err == nil {
			t.Fatalf("unsafe target %q was accepted", bad)
		}
	}
}
