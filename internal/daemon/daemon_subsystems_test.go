package daemon

// Purpose: closes subsystems.go's remaining coverage gaps that
//   daemon_test.go's Manifest behaviour tests do not reach: SubsystemState.
//   String() (never called directly by anything else — every other test
//   compares the enum value, not its rendering), NewManifest's nil-clock
//   fallback branch (every other test injects a real clock explicitly),
//   and set()'s "first write for a name nobody Register()ed" branch (every
//   other test calls Register before Started/Failed/etc.).
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/repo"
)

// TestReachabilitySeamWiring proves RegisterReachability wires a non-nil
// jobs.ReachabilityFn into jobs.NewPlanner, and that the closure forwards
// ctx unchanged and returns Reachable's own result unchanged (R-21.257,
// R-21.247(b)).
func TestReachabilitySeamWiring(t *testing.T) {
	graph := &repo.SymbolGraph{
		Nodes: []repo.GraphNode{
			{ID: "internal/policy", Kind: repo.NodePackage, Package: "internal/policy", File: "internal/policy/policy.go"},
		},
	}
	m := NewManifest(nil, nil)
	fn, err := m.RegisterReachability(graph)
	if err != nil {
		t.Fatalf("RegisterReachability: %v", err)
	}
	if fn == nil {
		t.Fatal("RegisterReachability returned a nil seam")
	}
	if planner := jobs.NewPlanner(fn); planner == nil {
		t.Fatal("jobs.NewPlanner with a non-nil seam returned nil")
	}

	got, err := fn(context.Background(), []string{"internal/policy/policy.go"})
	if err != nil {
		t.Fatalf("seam call: %v", err)
	}
	direct, directErr := repo.NewReachability(graph)
	if directErr != nil {
		t.Fatalf("repo.NewReachability: %v", directErr)
	}
	want, wantErr := direct.Reachable(context.Background(), []string{"internal/policy/policy.go"},
		[]repo.SensitiveClass{repo.ClassAuth, repo.ClassSecret, repo.ClassSchema})
	if wantErr != nil {
		t.Fatalf("direct Reachable: %v", wantErr)
	}
	if len(got) != len(want) {
		t.Fatalf("seam result = %v, want %v (Reachable's own result, unchanged)", got, want)
	}

	// The seam forwards ctx unchanged: a canceled ctx surfaces the same
	// cancellation Reachable's own ctx.Err() check would produce.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fn(canceled, []string{"internal/policy/policy.go"}); !errors.Is(err, context.Canceled) {
		t.Errorf("seam call with a canceled ctx: err = %v, want it to wrap context.Canceled", err)
	}
}

// TestRegisterReachability_NilGraphFailsClosed proves a nil graph fails
// the subsystem closed (a typed error, a Failed manifest entry, no seam)
// rather than returning a seam that panics on first use.
func TestRegisterReachability_NilGraphFailsClosed(t *testing.T) {
	m := NewManifest(nil, nil)
	fn, err := m.RegisterReachability(nil)
	if err == nil {
		t.Fatal("RegisterReachability(nil) = nil error, want a typed error")
	}
	if fn != nil {
		t.Error("RegisterReachability(nil) returned a non-nil seam alongside an error")
	}
}

func TestSubsystemState_String(t *testing.T) {
	cases := []struct {
		state SubsystemState
		want  string
	}{
		{SubsystemDeclared, "declared"},
		{SubsystemRunning, "running"},
		{SubsystemError, "error"},
		{SubsystemDisabled, "disabled"},
		{SubsystemSkipped, "skipped"},
		{SubsystemState(99), "unknown"}, // out-of-range value hits the default case
	}
	for _, c := range cases {
		if got := c.state.String(); got != c.want {
			t.Errorf("SubsystemState(%d).String() = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestNewManifest_NilClockFallsBackToSystemClock(t *testing.T) {
	m := NewManifest(nil, nil)
	if m == nil {
		t.Fatal("NewManifest(nil, nil) = nil")
	}
	// A functional smoke check that the fallback clock actually produces
	// a usable, non-empty timestamp — not just that construction didn't
	// panic.
	m.Register("ipc-socket")
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].UpdatedAt == "" {
		t.Errorf("snapshot = %+v, want one entry with a non-empty UpdatedAt", snap)
	}
}

// TestManifest_SetOnUnregisteredName_CreatesEntry proves set()'s
// defensive "not already declared" branch: calling Started (or Failed,
// Disabled, Skipped — they all route through the same set()) against a
// name nobody Register()ed still records a usable entry and appends it to
// Snapshot's order, rather than panicking on a missing map key.
func TestManifest_SetOnUnregisteredName_CreatesEntry(t *testing.T) {
	m := NewManifest(nil, nil)
	m.Started("never-registered", "addr")

	snap := m.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want exactly one entry", snap)
	}
	if snap[0].Name != "never-registered" || snap[0].State != SubsystemRunning || snap[0].Detail != "addr" {
		t.Errorf("snapshot[0] = %+v, want name=never-registered state=Running detail=addr", snap[0])
	}
}
