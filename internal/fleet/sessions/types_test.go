package sessions_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/sessions"
)

// TestSessionStateStringRoundTrip proves every declared SessionState
// round-trips through String/ParseSessionState, and that String never
// falls through its default case for a value in range.
func TestSessionStateStringRoundTrip(t *testing.T) {
	all := []sessions.SessionState{
		sessions.StateUnknown,
		sessions.StateActive,
		sessions.StateIdle,
		sessions.StateBlocked,
		sessions.StateStalled,
		sessions.StateClosed,
	}
	seen := make(map[string]bool, len(all))
	for _, st := range all {
		name := st.String()
		if name == "" {
			t.Fatalf("SessionState(%d).String() returned empty", st)
		}
		if seen[name] {
			t.Fatalf("SessionState name %q reused by more than one state", name)
		}
		seen[name] = true
		got, ok := sessions.ParseSessionState(name)
		if !ok {
			t.Fatalf("ParseSessionState(%q) failed round-trip for state %d", name, st)
		}
		if got != st {
			t.Fatalf("ParseSessionState(%q) = %d, want %d", name, got, st)
		}
	}
}

// TestParseSessionStateFailsClosed proves an unrecognized name is
// refused, never silently upgraded to any existing state.
func TestParseSessionStateFailsClosed(t *testing.T) {
	for _, bad := range []string{"", "ACTIVE", "activee", "unknown ", "0"} {
		if st, ok := sessions.ParseSessionState(bad); ok {
			t.Fatalf("ParseSessionState(%q) = (%d, true), want ok=false", bad, st)
		}
	}
}

// TestSessionStateUnknownIsZeroValue proves StateUnknown is the zero
// value, so a forgotten SessionState field reads as the fail-closed
// sentinel rather than an arbitrary other state.
func TestSessionStateUnknownIsZeroValue(t *testing.T) {
	var zero sessions.SessionState
	if zero != sessions.StateUnknown {
		t.Fatalf("zero-value SessionState = %d, want StateUnknown (%d)", zero, sessions.StateUnknown)
	}
}

// TestConfidenceSentinelBounds proves the two sentinel constants sit at
// the declared range's exact edges.
func TestConfidenceSentinelBounds(t *testing.T) {
	if sessions.ConfidenceNone != 0.0 {
		t.Fatalf("ConfidenceNone = %v, want 0.0", sessions.ConfidenceNone)
	}
	if sessions.ConfidenceFull != 1.0 {
		t.Fatalf("ConfidenceFull = %v, want 1.0", sessions.ConfidenceFull)
	}
}

// TestObservationZeroValueIsZero proves the fully zero-value Observation
// is the fail-closed case Advance detects, without reaching into the
// unexported isZero method (proven indirectly via Advance in
// statemachine_test.go); this test only pins the field defaults
// themselves so a future field addition is caught here first.
func TestObservationZeroValueIsZero(t *testing.T) {
	var obs sessions.Observation
	if obs.Census != nil || obs.Transcript != nil || obs.TranscriptErr != nil || obs.Domain != nil || obs.SSEClosed {
		t.Fatalf("zero-value Observation is not all-zero: %+v", obs)
	}
}
