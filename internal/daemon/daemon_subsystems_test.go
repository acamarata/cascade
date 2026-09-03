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
	"testing"
)

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
