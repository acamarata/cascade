// Purpose: Execute (the engine) integration tests. Exec is a
// fakeExecutor (a scripted stand-in for the real sub-process, already
// proven for real by runner_exec_test.go -- R-14.246: a fake replaces the
// EXTERNAL process only, never the code under test); DB is a live
// in-memory SQLite database through the SAME ApplyMigrationSchema/Upsert
// path T2's own domain_test.go exercises; Events and Journal are the
// REAL internal/events.Bus and internal/fleet/journal.SQLiteStore,
// backed by internal/storage/storetest's real (non-mocked) in-memory
// provider.Store -- so this suite proves Execute's actual integration
// with both real packages, not a hand-rolled substitute for their logic.
// SPORT: internal.ci.Execute/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fakeExecutor is a scripted Executor: command string -> ExecResult,
// recording every call IN ORDER (and the environment each was given) so a
// test can assert both the sequence the engine ran and that it halted
// before the remaining steps after a failure.
type fakeExecutor struct {
	results map[string]ExecResult
	calls   []string
	envs    [][]string
}

func (f *fakeExecutor) Run(_ context.Context, req ExecRequest) ExecResult {
	f.calls = append(f.calls, req.Command)
	f.envs = append(f.envs, req.Env)
	if r, ok := f.results[req.Command]; ok {
		return r
	}
	return ExecResult{ExitCode: 0}
}

// assertCallOrder asserts the executor was driven with exactly want, in
// order. Order is the assertion, not just the count: a count-only check
// passes for an engine that ran build before test.
func assertCallOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("executor calls = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("executor call %d = %q, want %q (full sequence %v)", i, got[i], want[i], got)
		}
	}
}

// newTestDeps builds a full Deps over real (non-fake) DB/Events/Journal
// backends, for the given Executor.
func newTestDeps(t *testing.T, exec Executor) Deps {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	clock := newTestClock()
	store := storetest.NewMemStore()
	return Deps{
		DB:      db,
		Events:  events.New(store, clock),
		Journal: journal.New(store, clock, journal.DefaultNamespace),
		Clock:   clock,
		Exec:    exec,
	}
}

// stepsInOrder builds a RunnerConfig with one RunnerStep per (kind,
// command) pair, always emitted in stepOrder regardless of map iteration
// order.
func stepsInOrder(cmds map[StepKind]string) RunnerConfig {
	var steps []RunnerStep
	for _, k := range stepOrder {
		if cmd, ok := cmds[k]; ok {
			steps = append(steps, RunnerStep{Kind: k, Command: cmd})
		}
	}
	return RunnerConfig{RepoRoot: "/repo", Steps: steps, Env: []string{"CI=true"}, TimeoutPerStep: time.Second}
}

func TestExecute_AllStepsPass(t *testing.T) {
	exec := &fakeExecutor{results: map[string]ExecResult{
		"lint-cmd": {ExitCode: 0}, "test-cmd": {ExitCode: 0}, "build-cmd": {ExitCode: 0},
	}}
	deps := newTestDeps(t, exec)
	cfg := stepsInOrder(map[StepKind]string{StepLint: "lint-cmd", StepTest: "test-cmd", StepBuild: "build-cmd"})
	cfg.OwnerRepo = "acamarata/cascade"
	ctx := context.Background()

	result, err := Execute(ctx, deps, cfg, 1001, 2001, "myrepo")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Passed() {
		t.Fatalf("result.Passed() = false, want true: %+v", result)
	}
	assertCallOrder(t, exec.calls, []string{"lint-cmd", "test-cmd", "build-cmd"})

	src, err := runSource(ctx, deps.DB, 1001, 2001)
	if err != nil {
		t.Fatalf("runSource: %v", err)
	}
	if src != SourceLocal {
		t.Errorf("runSource = %q, want %q", src, SourceLocal)
	}

	// Acceptance criterion: "the domain event fires and is observable."
	published, err := deps.Events.Replay(ctx, EventNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(published) != 1 || published[0].Kind != EventKindRunCompleted {
		t.Fatalf("Replay = %+v, want exactly one EventKindRunCompleted event", published)
	}
	var payload runCompletedPayload
	if err := json.Unmarshal(published[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal ci.run.completed payload: %v", err)
	}
	if payload.Repo != cfg.OwnerRepo {
		t.Errorf("payload.Repo = %q, want %q (RunnerConfig.OwnerRepo)", payload.Repo, cfg.OwnerRepo)
	}
}

func TestExecute_HaltsOnFirstFailure(t *testing.T) {
	exec := &fakeExecutor{results: map[string]ExecResult{
		"lint-cmd": {ExitCode: 0}, "test-cmd": {ExitCode: 1}, "build-cmd": {ExitCode: 0},
	}}
	deps := newTestDeps(t, exec)
	cfg := stepsInOrder(map[StepKind]string{StepLint: "lint-cmd", StepTest: "test-cmd", StepBuild: "build-cmd"})

	result, err := Execute(context.Background(), deps, cfg, 1002, 2002, "myrepo")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Passed() {
		t.Fatal("result.Passed() = true, want false (the test step failed)")
	}
	if result.FailedStep != StepTest {
		t.Errorf("FailedStep = %q, want %q", result.FailedStep, StepTest)
	}
	// Order, not just count: build must never run, and lint must have run
	// BEFORE test rather than merely having run.
	assertCallOrder(t, exec.calls, []string{"lint-cmd", "test-cmd"})

	// The halted run must be readable the way `cascade ci status` reads it:
	// a failed conclusion, attributed to the local producer.
	runs, err := ListRuns(context.Background(), deps.DB, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	var found bool
	for _, r := range runs {
		if r.RunID != result.RunID {
			continue
		}
		found = true
		if r.Conclusion != ConclusionFailure {
			t.Errorf("halted run conclusion = %q, want %q", r.Conclusion, ConclusionFailure)
		}
		if r.Source != SourceLocal {
			t.Errorf("halted run source = %q, want %q", r.Source, SourceLocal)
		}
		if r.Status != RunStatusCompleted {
			t.Errorf("halted run status = %q, want %q", r.Status, RunStatusCompleted)
		}
	}
	if !found {
		t.Fatalf("ListRuns returned no row for the halted run %d: %+v", result.RunID, runs)
	}
}

// TestExecute_JournalsStartAndResult proves each executed step is
// journaled twice (KindIntent on start, KindAck on result) through the
// REAL journal.SQLiteStore.
func TestExecute_JournalsStartAndResult(t *testing.T) {
	exec := &fakeExecutor{results: map[string]ExecResult{"lint-cmd": {ExitCode: 0}}}
	deps := newTestDeps(t, exec)
	cfg := stepsInOrder(map[StepKind]string{StepLint: "lint-cmd"})
	ctx := context.Background()

	result, err := Execute(ctx, deps, cfg, 1003, 2003, "myrepo")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	entries, err := deps.Journal.(*journal.SQLiteStore).Replay(ctx, journalEntityID(result.RunID, result.RepoID), journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("journal entries = %d, want 2 (one Intent, one Ack)", len(entries))
	}
	if entries[0].Kind != journal.KindIntent || entries[1].Kind != journal.KindAck {
		t.Errorf("journal kinds = [%v, %v], want [Intent, Ack]", entries[0].Kind, entries[1].Kind)
	}
}

func TestExecute_RequiresDeps(t *testing.T) {
	if _, err := Execute(context.Background(), Deps{}, RunnerConfig{}, 1, 2, "x"); err == nil {
		t.Fatal("expected an error for an empty Deps")
	}
}

// TestExecute_NilEventsAndJournalAreOptional proves a Run with no Events
// bus and no JournalStore injected still completes and still writes
// ci_results -- optional side channels never gate the actual gate.
func TestExecute_NilEventsAndJournalAreOptional(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	exec := &fakeExecutor{results: map[string]ExecResult{"cmd": {ExitCode: 0}}}
	deps := Deps{DB: db, Clock: newTestClock(), Exec: exec}
	cfg := RunnerConfig{RepoRoot: "/repo", Steps: []RunnerStep{{Kind: StepLint, Command: "cmd"}}, Env: []string{"CI=true"}, TimeoutPerStep: time.Second}

	result, err := Execute(ctx, deps, cfg, 1, 2, "x")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Passed() {
		t.Fatal("want Passed()")
	}
}

// TestExecute_StepStartErrorFails proves a step that cannot even start
// (StartErr set, matching the real ShellExecutor's "command not found"
// class of failure) is treated as a failed step.
func TestExecute_StepStartErrorFails(t *testing.T) {
	exec := &fakeExecutor{results: map[string]ExecResult{
		"bad-cmd": {StartErr: context.DeadlineExceeded, ExitCode: -1},
	}}
	deps := newTestDeps(t, exec)
	cfg := stepsInOrder(map[StepKind]string{StepLint: "bad-cmd"})

	result, err := Execute(context.Background(), deps, cfg, 1004, 2004, "x")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Passed() {
		t.Fatal("a StartErr step must not report Passed()")
	}
	if result.FailedStep != StepLint {
		t.Errorf("FailedStep = %q, want %q", result.FailedStep, StepLint)
	}
}
