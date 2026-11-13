package write

import "testing"

// TestGreensDecreasedClosesOnCLIRebase verifies that a successful CLI rebase closes
// an open or reopened greens lifecycle finding with nil error, and that subsequent
// ordinary ticks keep it closed. Severity remains "error" throughout.
func TestGreensDecreasedClosesOnCLIRebase(t *testing.T) {
	t.Run("cliRebase closes open state", func(t *testing.T) {
		lc := NewGreensLifecycle()
		if lc.State != GreensOpen {
			t.Fatalf("expected initial state open, got %s", lc.State)
		}

		result, err := TickGreens(lc, true)
		if err != nil {
			t.Errorf("expected nil error on cliRebase=true, got %v", err)
		}
		if result.State != GreensClosed {
			t.Errorf("expected state closed after cliRebase, got %s", result.State)
		}
		if result.Severity != "error" {
			t.Errorf("expected severity error, got %s", result.Severity)
		}
	})

	t.Run("cliRebase closes reopened state", func(t *testing.T) {
		lc := NewGreensLifecycle()
		lc.State = GreensReopened

		result, err := TickGreens(lc, true)
		if err != nil {
			t.Errorf("expected nil error on cliRebase=true, got %v", err)
		}
		if result.State != GreensClosed {
			t.Errorf("expected state closed after cliRebase, got %s", result.State)
		}
		if result.Severity != "error" {
			t.Errorf("expected severity error, got %s", result.Severity)
		}
	})

	t.Run("closed stays closed on subsequent ticks", func(t *testing.T) {
		lc := NewGreensLifecycle()
		lc.State = GreensClosed

		result, err := TickGreens(lc, false)
		if err != nil {
			t.Errorf("expected nil error for closed state, got %v", err)
		}
		if result.State != GreensClosed {
			t.Errorf("expected state to remain closed, got %s", result.State)
		}
		if result.Severity != "error" {
			t.Errorf("expected severity error, got %s", result.Severity)
		}
	})

	t.Run("end to end flow", func(t *testing.T) {
		lc := NewGreensLifecycle()

		// First tick without rebase: should reopen with error
		tick1, err1 := TickGreens(lc, false)
		if err1 != ErrGreensDecreased {
			t.Errorf("expected ErrGreensDecreased on first tick, got %v", err1)
		}
		if tick1.State != GreensReopened {
			t.Errorf("expected state reopened after first tick, got %s", tick1.State)
		}

		// CLI rebase closes the finding
		tick2, err2 := TickGreens(tick1, true)
		if err2 != nil {
			t.Errorf("expected nil error on cliRebase, got %v", err2)
		}
		if tick2.State != GreensClosed {
			t.Errorf("expected state closed after cliRebase, got %s", tick2.State)
		}
		if tick2.Severity != "error" {
			t.Errorf("expected severity error after close, got %s", tick2.Severity)
		}

		// Subsequent ordinary tick stays closed
		tick3, err3 := TickGreens(tick2, false)
		if err3 != nil {
			t.Errorf("expected nil error for closed state, got %v", err3)
		}
		if tick3.State != GreensClosed {
			t.Errorf("expected state to remain closed, got %s", tick3.State)
		}
		if tick3.Severity != "error" {
			t.Errorf("expected severity error, got %s", tick3.Severity)
		}
	})
}
