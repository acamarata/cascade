// Purpose: Dispatch and the events and callbacks it produces, against the
// shared rig (stream_test.go) with the REAL local executor over a scripted
// shell, so every sub-job writes its ci_run and ci_job rows through
// Execute.
//
// SPORT: internal.ci.Dispatcher.Dispatch/TESTED (P1-CI-01).
package ci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDispatchStorageRefusalsPreserveReplayState(t *testing.T) {
	for _, stage := range []string{"record", "state", "intent"} {
		t.Run(stage, func(t *testing.T) {
			r := newRig(t)
			ctx := context.Background()
			if err := r.d.openAttempt(ctx, attemptRow{AttemptID: "a", JobID: r.ref.JobID}); err != nil {
				t.Fatal(err)
			}
			query, message, kind, entries := dispatchRefusal(stage)
			if _, err := r.ciDB.Exec(query); err != nil {
				t.Fatal(err)
			}
			if stage == "intent" {
				if err := r.jobsDB.Close(); err != nil {
					t.Fatal(err)
				}
			}
			result, err := r.d.Dispatch(ctx, r.ref, CandidateSnapshot{AttemptID: "a"}, CIRequirementPlan{Requirement: CIRequirement{Unit: true}}, false)
			assertStreamError(t, err, kind, message)
			if len(result) != 0 || r.exec.count() != 0 {
				t.Fatalf("refused dispatch executed work: %+v, %d", result, r.exec.count())
			}
			if stage == "record" {
				if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt WHERE dispatches = 'corrupt'`); n != 1 {
					t.Fatal("refusal replaced corrupt state")
				}
				return
			}
			a, ok, err := r.d.getAttempt(ctx, "a")
			wantState := attemptLive
			if stage == "state" {
				wantState = attemptTerminal
			}
			if err != nil || !ok || a.State != wantState || len(a.Entries) != entries {
				t.Fatalf("refusal lost replay state: %+v, %v, %v", a, ok, err)
			}
		})
	}
}

// dispatchRefusal provides a deterministic failure at each pre-execution write.
func dispatchRefusal(stage string) (query, message string, kind cascade.Kind, entries int) {
	switch stage {
	case "record":
		return `UPDATE ci_stream_attempt SET dispatches = 'corrupt'`, "unreadable dispatches", cascade.KindIntegrity, 0
	case "state":
		return `UPDATE ci_stream_attempt SET state = 'terminal';
			CREATE TRIGGER refuse_state BEFORE UPDATE OF state ON ci_stream_attempt
			BEGIN SELECT RAISE(ABORT, 'state refused'); END`, "set attempt state", cascade.KindUnavailable, 1
	default:
		return `SELECT 1`, "begin outbox transaction", cascade.KindUnavailable, 1
	}
}

// lockedExec is a ci.Executor that records every command, safe for the
// parallel sub-jobs of one dispatch.
type lockedExec struct {
	mu    sync.Mutex
	calls []ExecRequest
	fn    func(ExecRequest) ExecResult
}

func (l *lockedExec) Run(_ context.Context, req ExecRequest) ExecResult {
	l.mu.Lock()
	l.calls = append(l.calls, req)
	fn := l.fn
	l.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return ExecResult{}
}

func (l *lockedExec) commands() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, c := range l.calls {
		out = append(out, c.Command)
	}
	return out
}

func allKindCommands() map[RequirementKind][]string {
	m := map[RequirementKind][]string{}
	for _, k := range allRequirementKinds {
		m[k] = []string{"run-" + string(k)}
	}
	return m
}

// useLocal swaps the rig's executor for the real local executor over fx.
func (r *streamRig) useLocal(fx Executor, onEnv func(Environment)) {
	r.t.Helper()
	ex, err := NewLocalSubJobExecutor(LocalExecutorDeps{
		CIDB: r.ciDB, Events: r.bus, Clock: newTestClock(), Exec: fx, Commands: allKindCommands(),
		Environ: []string{"PATH=" + os.Getenv("PATH")}, RunRoot: r.t.TempDir(), ModCache: filepath.Join(r.t.TempDir(), "mod"),
		Populate:      func(context.Context, string, []string) error { return nil },
		OnEnvironment: onEnv,
	})
	if err != nil {
		r.t.Fatalf("NewLocalSubJobExecutor: %v", err)
	}
	r.build(ex)
}

func streamRunNames(r *streamRig) []string {
	r.t.Helper()
	rows, err := r.ciDB.Query(`SELECT name FROM ci_run WHERE name LIKE 'stream-%' ORDER BY name`)
	if err != nil {
		r.t.Fatalf("query ci_run: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			r.t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	return out
}

func kindSuffixes(names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, n[strings.LastIndex(n, "-")+1:])
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

type planKindsCase struct {
	name  string
	files map[string]string
	want  []string
}

var planKindsCases = []planKindsCase{
	{"docs only is the partial low set", map[string]string{"docs/a.md": "a"}, []string{"format", "lint"}},
	{"source change adds build, unit and integration", map[string]string{"src/a.go": "package a\n"},
		[]string{"format", "lint", "compile", "unit", "integration"}},
	{"pkg change is the full set", map[string]string{"pkg/a.go": "package a\n"},
		[]string{"format", "lint", "compile", "unit", "integration", "architecture", "security"}},
}

// assertDispatchedRows checks the ci rows one checkpoint left: one ci_run,
// ci_job, source=local row and via_stream=1 marker per wanted kind.
func assertDispatchedRows(t *testing.T, r *streamRig, snap CandidateSnapshot, want []string) {
	t.Helper()
	if names := streamRunNames(r); !sameSet(kindSuffixes(names), want) {
		t.Fatalf("dispatched kinds = %v, want exactly %v", kindSuffixes(names), want)
	}
	for what, query := range map[string]string{
		"completed ci_job":   `SELECT COUNT(*) FROM ci_job WHERE status = 'completed'`,
		"source=local":       `SELECT COUNT(*) FROM ci_run_source WHERE source = 'local'`,
		"via_stream=1 (ckp)": `SELECT COUNT(*) FROM ci_run_stream WHERE via_stream = 1 AND checkpoint_id = '` + snap.CheckpointID + `'`,
	} {
		if got := r.count(r.ciDB, query); got != len(want) {
			t.Fatalf("%s rows = %d, want %d", what, got, len(want))
		}
	}
}

func TestCheckpointDispatchesPlanKinds(t *testing.T) {
	for _, tc := range planKindsCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.useLocal(&lockedExec{}, nil)
			r.commit(tc.files)
			snap, err := r.checkpoint()
			if err != nil {
				t.Fatalf("Checkpoint: %v", err)
			}
			assertDispatchedRows(t, r, snap, tc.want)
		})
	}
	t.Run("a partial Dispatch plan runs exactly its kinds", func(t *testing.T) {
		r := newRig(t)
		r.commit(map[string]string{"docs/a.md": "a"})
		snap, err := r.checkpoint()
		if err != nil {
			t.Fatalf("Checkpoint: %v", err)
		}
		before := r.exec.count()
		plan := CIRequirementPlan{Requirement: CIRequirement{Integration: true}, RiskClass: "low"}
		evs, err := r.d.Dispatch(context.Background(), r.ref, snap, plan, false)
		if err != nil || len(evs) != 1 || evs[0].Kind != RequirementIntegration {
			t.Fatalf("Dispatch = %+v, %v; want exactly the integration kind", evs, err)
		}
		if got := r.exec.count() - before; got != 1 {
			t.Fatalf("executor ran %d sub-jobs, want 1", got)
		}
	})
}
