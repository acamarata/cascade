package background

import "testing"

// TestSwarmResultCarriesMappedAttempt checks that a SwarmResult holds what MapAttempt produces for a failed attempt.
func TestSwarmResultCarriesMappedAttempt(t *testing.T) {
	ev, out := MapAttempt(Attempt{N: 1, Err: "boom"})
	r := SwarmResult{Events: []Event{ev}, Outcome: out, NextBrief: "b2"}
	if len(r.Events) != 1 || r.Events[0] != ev || r.Outcome != out || r.NextBrief != "b2" {
		t.Fatalf("SwarmResult = %+v", r)
	}
	if r.Outcome.Status != "needs-owner" || r.Outcome.Detail != "boom" {
		t.Fatalf("outcome = %+v, want needs-owner with the error text", r.Outcome)
	}
}
