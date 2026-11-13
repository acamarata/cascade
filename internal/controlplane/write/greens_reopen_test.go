package write

import (
	"errors"
	"testing"
)

func TestGreensDecreasedReopensEachTick(t *testing.T) {
	lc := NewGreensLifecycle()

	// First tick from open
	out1, err1 := TickGreens(lc, false)
	if out1.State != GreensReopened {
		t.Fatalf("expected state reopened, got %s", out1.State)
	}
	if out1.Severity != "error" {
		t.Fatalf("expected severity error, got %s", out1.Severity)
	}
	if !errors.Is(err1, ErrGreensDecreased) {
		t.Fatalf("expected ErrGreensDecreased, got %v", err1)
	}
	if err1.Error() != "D1089" {
		t.Fatalf("expected error text D1089, got %s", err1.Error())
	}

	// Second tick from reopened
	out2, err2 := TickGreens(out1, false)
	if out2.State != GreensReopened {
		t.Fatalf("expected state reopened, got %s", out2.State)
	}
	if out2.Severity != "error" {
		t.Fatalf("expected severity error, got %s", out2.Severity)
	}
	if !errors.Is(err2, ErrGreensDecreased) {
		t.Fatalf("expected ErrGreensDecreased, got %v", err2)
	}
}

func TestGreensDecreasedStaysOpenWithoutRebase(t *testing.T) {
	lc := NewGreensLifecycle()
	lc.State = GreensReopened

	// The input value is unchanged
	original := lc

	out, err := TickGreens(lc, false)
	if out.State != GreensReopened {
		t.Fatalf("expected state reopened, got %s", out.State)
	}
	if !errors.Is(err, ErrGreensDecreased) {
		t.Fatalf("expected ErrGreensDecreased, got %v", err)
	}

	// Input is unchanged
	if lc != original {
		t.Fatalf("input was mutated")
	}

	// Multiple ticks
	for i := 0; i < 3; i++ {
		out, err = TickGreens(out, false)
		if out.State != GreensReopened {
			t.Fatalf("expected state reopened on tick %d, got %s", i, out.State)
		}
		if !errors.Is(err, ErrGreensDecreased) {
			t.Fatalf("expected ErrGreensDecreased on tick %d, got %v", i, err)
		}
	}
}
