package conductpolicy

import "testing"

func TestReasonValid(t *testing.T) {
	validReasons := []Reason{
		ReasonHardProblem,
		ReasonConsult,
		ReasonAdversarialReview,
		ReasonRoutine,
	}
	for _, r := range validReasons {
		if !r.Valid() {
			t.Errorf("Reason(%q).Valid() = false, want true", r)
		}
	}

	invalidReasons := []Reason{"", "hard", "Consult"}
	for _, r := range invalidReasons {
		if r.Valid() {
			t.Errorf("Reason(%q).Valid() = true, want false", r)
		}
	}
}

func TestTierValid(t *testing.T) {
	validTiers := []Tier{
		TierStandard,
		TierHigh,
	}
	for _, tier := range validTiers {
		if !tier.Valid() {
			t.Errorf("Tier(%d).Valid() = false, want true", tier)
		}
	}

	invalidTiers := []Tier{Tier(-1), Tier(2)}
	for _, tier := range invalidTiers {
		if tier.Valid() {
			t.Errorf("Tier(%d).Valid() = true, want false", tier)
		}
	}
}
