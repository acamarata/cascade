package background

import (
	"errors"
	"testing"
)

func TestRolloutCapExact(t *testing.T) {
	l := NewLedger()

	// Nonparallel Rollouts=4000 returns VerdictAccepted, nil
	v, err := l.Admit(Request{Rollouts: MaxRollouts, Improvements: 0})
	if v != VerdictAccepted || err != nil {
		t.Errorf("expected accepted for Rollouts=%d, got %v, %v", MaxRollouts, v, err)
	}

	// Zero and lower positive values accepted
	for _, rollouts := range []int{0, 1, 1000, 3999} {
		v, err = l.Admit(Request{Rollouts: rollouts, Improvements: 0})
		if v != VerdictAccepted || err != nil {
			t.Errorf("expected accepted for Rollouts=%d, got %v, %v", rollouts, v, err)
		}
	}

	// Repeated requests are independent
	v, err = l.Admit(Request{Rollouts: MaxRollouts, Improvements: 0})
	if v != VerdictAccepted || err != nil {
		t.Errorf("expected accepted for repeated Rollouts=%d, got %v, %v", MaxRollouts, v, err)
	}

	// Nil receiver matches ErrInvalidBudget
	var nilL *Ledger
	v, err = nilL.Admit(Request{Rollouts: 0, Improvements: 0})
	if v != VerdictRejected || !errors.Is(err, ErrInvalidBudget) {
		t.Errorf("expected rejected and ErrInvalidBudget for nil receiver, got %v, %v", v, err)
	}

	// Over cap
	v, err = l.Admit(Request{Rollouts: MaxRollouts + 1, Improvements: 0})
	if v != VerdictRejected || !errors.Is(err, ErrOverage) {
		t.Errorf("expected rejected and ErrOverage for Rollouts=%d, got %v, %v", MaxRollouts+1, v, err)
	}
}

func TestImprovementCapExact(t *testing.T) {
	l := NewLedger()

	// Nonparallel Improvements=3000 returns VerdictAccepted, nil
	v, err := l.Admit(Request{Rollouts: 0, Improvements: MaxImprovements})
	if v != VerdictAccepted || err != nil {
		t.Errorf("expected accepted for Improvements=%d, got %v, %v", MaxImprovements, v, err)
	}

	// Zero and lower positive values accepted
	for _, improvements := range []int{0, 1, 1000, 2999} {
		v, err = l.Admit(Request{Rollouts: 0, Improvements: improvements})
		if v != VerdictAccepted || err != nil {
			t.Errorf("expected accepted for Improvements=%d, got %v, %v", improvements, v, err)
		}
	}

	// Negative count of either kind matches ErrInvalidBudget without effects
	v, err = l.Admit(Request{Rollouts: -1, Improvements: 0})
	if v != VerdictRejected || !errors.Is(err, ErrInvalidBudget) {
		t.Errorf("expected rejected and ErrInvalidBudget for negative Rollouts, got %v, %v", v, err)
	}

	v, err = l.Admit(Request{Rollouts: 0, Improvements: -1})
	if v != VerdictRejected || !errors.Is(err, ErrInvalidBudget) {
		t.Errorf("expected rejected and ErrInvalidBudget for negative Improvements, got %v, %v", v, err)
	}

	// Verify no effects: valid request should still be accepted
	v, err = l.Admit(Request{Rollouts: 0, Improvements: MaxImprovements})
	if v != VerdictAccepted || err != nil {
		t.Errorf("expected accepted after negative request, got %v, %v", v, err)
	}

	// Over cap
	v, err = l.Admit(Request{Rollouts: 0, Improvements: MaxImprovements + 1})
	if v != VerdictRejected || !errors.Is(err, ErrOverage) {
		t.Errorf("expected rejected and ErrOverage for Improvements=%d, got %v, %v", MaxImprovements+1, v, err)
	}
}
