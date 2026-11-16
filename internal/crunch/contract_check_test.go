package crunch

import (
	"errors"
	"testing"
)

// TestCheckSymbols verifies that contract symbols are checked correctly.
func TestCheckSymbols(t *testing.T) {
	t.Run("unresolved contract returns ErrUnresolvedContract", func(t *testing.T) {
		sc := &SymbolChecker{
			Provided: map[string]bool{},
		}
		ticket := "This ticket references @CONTRACT_A and @CONTRACT_B."
		err := sc.CheckSymbols(ticket)
		if err == nil {
			t.Fatal("expected error for unresolved contracts, got nil")
		}
		if !errors.Is(err, ErrUnresolvedContract) {
			t.Fatalf("expected ErrUnresolvedContract, got %v", err)
		}
	})

	t.Run("all contracts provided returns nil", func(t *testing.T) {
		sc := &SymbolChecker{
			Provided: map[string]bool{
				"CONTRACT_A": true,
				"CONTRACT_B": true,
			},
		}
		ticket := "This ticket references @CONTRACT_A and @CONTRACT_B."
		err := sc.CheckSymbols(ticket)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("symbols checked only for provided contracts", func(t *testing.T) {
		sc := &SymbolChecker{
			Provided: map[string]bool{
				"CONTRACT_A": true,
			},
		}
		// CONTRACT_B is not in Provided, so it should be flagged
		ticket := "This ticket references @CONTRACT_A and @CONTRACT_B."
		err := sc.CheckSymbols(ticket)
		if err == nil {
			t.Fatal("expected error for CONTRACT_B not provided")
		}
		if !errors.Is(err, ErrUnresolvedContract) {
			t.Fatalf("expected ErrUnresolvedContract, got %v", err)
		}
	})

	t.Run("no contract references returns nil", func(t *testing.T) {
		sc := &SymbolChecker{
			Provided: map[string]bool{},
		}
		ticket := "This ticket has no contract references."
		err := sc.CheckSymbols(ticket)
		if err != nil {
			t.Fatalf("expected nil for no references, got %v", err)
		}
	})

	t.Run("partial contracts provided returns error", func(t *testing.T) {
		sc := &SymbolChecker{
			Provided: map[string]bool{
				"CONTRACT_A": true,
			},
		}
		ticket := "References @CONTRACT_A, @CONTRACT_B, and @CONTRACT_C."
		err := sc.CheckSymbols(ticket)
		if err == nil {
			t.Fatal("expected error for missing contracts")
		}
		if !errors.Is(err, ErrUnresolvedContract) {
			t.Fatalf("expected ErrUnresolvedContract, got %v", err)
		}
	})
}

// TestResetRetries verifies that ResetRetries clears contract retry state.
func TestResetRetries(t *testing.T) {
	RecordRetry("CONTRACT_A")
	RecordRetry("CONTRACT_A")
	if GetRetryCount("CONTRACT_A") != 2 {
		t.Fatalf("expected 2 retries before reset, got %d", GetRetryCount("CONTRACT_A"))
	}

	ResetRetries()

	if GetRetryCount("CONTRACT_A") != 0 {
		t.Fatalf("expected 0 retries after reset, got %d", GetRetryCount("CONTRACT_A"))
	}
}
