package flow

import "testing"

// TestTickWireValue pins the wire value of the Tick event kind, which a later slice feeds to Step.
func TestTickWireValue(t *testing.T) {
	if Tick != EventKind("tick") {
		t.Fatalf("Tick = %q, want %q", Tick, "tick")
	}
}
