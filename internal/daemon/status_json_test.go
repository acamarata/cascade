package daemon

// Purpose: mutation-provable coverage for SubsystemState's MarshalJSON/
//   UnmarshalJSON (status.go) - the status.get wire encoding that lets a
//   client read Subsystems[i].State as "skipped"/"disabled"/"error"
//   instead of an opaque small integer, per that method's own doc comment.
//   Split from status_test.go to respect the 300-line file cap.
// SPORT: internal/daemon (ADD, per T-1 sport_updates).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestSubsystemState_JSONRoundTrip proves MarshalJSON/UnmarshalJSON are
// true inverses over every declared SubsystemState: the wire form is the
// state's String() name (e.g. "skipped"), and decoding that name back
// produces the exact same state, never a shifted or truncated one.
func TestSubsystemState_JSONRoundTrip(t *testing.T) {
	cases := []struct {
		state SubsystemState
		wire  string
	}{
		{SubsystemDeclared, `"declared"`},
		{SubsystemRunning, `"running"`},
		{SubsystemError, `"error"`},
		{SubsystemDisabled, `"disabled"`},
		{SubsystemSkipped, `"skipped"`},
	}
	for _, tc := range cases {
		t.Run(tc.wire, func(t *testing.T) {
			raw, err := json.Marshal(tc.state)
			if err != nil {
				t.Fatalf("MarshalJSON(%v): %v", tc.state, err)
			}
			if string(raw) != tc.wire {
				t.Fatalf("MarshalJSON(%v) = %s, want %s", tc.state, raw, tc.wire)
			}

			var got SubsystemState
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", raw, err)
			}
			if got != tc.state {
				t.Fatalf("round trip of %s = state %v, want %v", raw, got, tc.state)
			}
		})
	}
}

// TestSubsystemState_UnmarshalJSON_UnknownNameErrors proves an
// unrecognized wire value is a decode error, not a silent zero-value
// state: a status.get envelope naming a state this build doesn't know
// must fail loud, never masquerade as SubsystemDeclared (the zero value),
// which would misreport an unknown state as "not yet attempted".
//
// MUTATION PROOF (recorded in full in the journal): changing
// UnmarshalJSON's default case from `return fmt.Errorf(...)` to `return
// nil` turns this red with the real message
// "UnmarshalJSON(\"quantum-entangled\"): want an error for an
// unrecognized state name, got nil (s=declared)" - the exact silent-
// zero-value failure this test exists to catch. Reverting restores GREEN.
func TestSubsystemState_UnmarshalJSON_UnknownNameErrors(t *testing.T) {
	var s SubsystemState
	err := json.Unmarshal([]byte(`"quantum-entangled"`), &s)
	if err == nil {
		t.Fatalf("UnmarshalJSON(%q): want an error for an unrecognized state name, got nil (s=%v)", "quantum-entangled", s)
	}
	const want = `daemon: unknown subsystem state "quantum-entangled"`
	if err.Error() != want {
		t.Fatalf("UnmarshalJSON error = %q, want %q", err.Error(), want)
	}
}

// TestSubsystemState_UnmarshalJSON_NonStringJSONErrors proves a malformed
// wire value (not even a JSON string) surfaces json.Unmarshal's own type
// error rather than being swallowed or decoded as a bogus state.
func TestSubsystemState_UnmarshalJSON_NonStringJSONErrors(t *testing.T) {
	var s SubsystemState
	if err := json.Unmarshal([]byte(`42`), &s); err == nil {
		t.Fatalf("UnmarshalJSON(42): want an error decoding a non-string value, got nil (s=%v)", s)
	}
}

// TestStatusGet_ResponseJSONRoundTrip proves the full status.get envelope
// survives a real JSON encode/decode, including a non-empty Subsystems
// list whose SubsystemState values ride the wire as their String() name -
// the property status.go's MarshalJSON doc comment promises callers: "a
// caller reading Subsystems over the wire sees
// \"skipped\"/\"disabled\"/\"error\", not an opaque small integer."
// Asserts on the actual marshaled bytes and the actually decoded struct,
// not that marshaling merely succeeded.
func TestStatusGet_ResponseJSONRoundTrip(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	manifest := NewManifest(nil, clock)
	manifest.Register("a")
	manifest.Started("a", "ok")
	manifest.Register("b")
	manifest.Skipped("b", "no graph yet")

	provider := NewStatusProvider(clock, clock.Now(), "/tmp/d.sock", nil, manifest)
	result, err := provider.Handler()(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	resp := result.(StatusResponse)

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal(StatusResponse): %v", err)
	}
	if !strings.Contains(string(raw), `"skipped"`) {
		t.Fatalf("marshaled envelope = %s, want it to contain the string state \"skipped\", not an integer", raw)
	}

	var decoded StatusResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(StatusResponse): %v", err)
	}
	if len(decoded.Subsystems) != len(resp.Subsystems) {
		t.Fatalf("decoded Subsystems len = %d, want %d", len(decoded.Subsystems), len(resp.Subsystems))
	}
	for i, want := range resp.Subsystems {
		if got := decoded.Subsystems[i]; got != want {
			t.Fatalf("decoded Subsystems[%d] = %+v, want %+v (real round trip, not a stub)", i, got, want)
		}
	}
}
