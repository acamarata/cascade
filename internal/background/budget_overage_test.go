package background

import (
	"errors"
	"testing"
)

func TestRejectRolloutOverage(t *testing.T) {
	l := NewLedger()
	r := Request{
		Rollouts:     MaxRollouts + 1,
		Improvements: 0,
		Parallel:     false,
	}
	v, err := l.Admit(r)
	if v != VerdictRejected {
		t.Errorf("expected VerdictRejected, got %v", v)
	}
	if !errors.Is(err, ErrOverage) {
		t.Errorf("expected ErrOverage, got %v", err)
	}
	if err != nil && err.Error() != "budget overage" {
		t.Errorf("expected error text 'budget overage', got %q", err.Error())
	}
}

func TestRejectImprovementOverage(t *testing.T) {
	l := NewLedger()
	r := Request{
		Rollouts:     0,
		Improvements: MaxImprovements + 1,
		Parallel:     false,
	}
	v, err := l.Admit(r)
	if v != VerdictRejected {
		t.Errorf("expected VerdictRejected, got %v", v)
	}
	if !errors.Is(err, ErrOverage) {
		t.Errorf("expected ErrOverage, got %v", err)
	}
}
