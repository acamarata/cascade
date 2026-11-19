package consult

import "testing"

// TestHoldsTwoYesZeroNo verifies that two yes votes and zero no votes hold.
func TestHoldsTwoYesZeroNo(t *testing.T) {
	votes := []Vote{
		{Voter: "a", HOLDS: "yes"},
		{Voter: "b", HOLDS: "yes"},
	}

	if got := HoldsDecision(votes); !got {
		t.Errorf("HoldsDecision(%v) = %v, want true", votes, got)
	}
}

// TestHoldsOneYesFails verifies that a single yes vote is not enough to hold.
func TestHoldsOneYesFails(t *testing.T) {
	votes := []Vote{
		{Voter: "a", HOLDS: "yes"},
	}

	if got := HoldsDecision(votes); got {
		t.Errorf("HoldsDecision(%v) = %v, want false", votes, got)
	}
}

// TestHoldsTieFails verifies that an equal number of yes and no votes fails.
func TestHoldsTieFails(t *testing.T) {
	votes := []Vote{
		{Voter: "a", HOLDS: "yes"},
		{Voter: "b", HOLDS: "yes"},
		{Voter: "c", HOLDS: "no"},
		{Voter: "d", HOLDS: "no"},
	}

	if got := HoldsDecision(votes); got {
		t.Errorf("HoldsDecision(%v) = %v, want false", votes, got)
	}
}

// TestHoldsMajorityWithNo verifies that three yes votes and one no vote hold.
func TestHoldsMajorityWithNo(t *testing.T) {
	votes := []Vote{
		{Voter: "a", HOLDS: "yes"},
		{Voter: "b", HOLDS: "yes"},
		{Voter: "c", HOLDS: "yes"},
		{Voter: "d", HOLDS: "no"},
	}

	if got := HoldsDecision(votes); !got {
		t.Errorf("HoldsDecision(%v) = %v, want true", votes, got)
	}
}

// TestHoldsEmptyFailsClosed verifies that no votes fails closed.
func TestHoldsEmptyFailsClosed(t *testing.T) {
	if got := HoldsDecision(nil); got {
		t.Errorf("HoldsDecision(nil) = %v, want false", got)
	}

	votes := []Vote{}
	if got := HoldsDecision(votes); got {
		t.Errorf("HoldsDecision(%v) = %v, want false", votes, got)
	}
}
