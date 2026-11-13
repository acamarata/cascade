package consult

import (
	"errors"
	"strings"
	"testing"
)

// TestGCIBlockWithinBudget verifies that assemblies within the 1200 token budget succeed.
func TestGCIBlockWithinBudget(t *testing.T) {
	// Test case 1: exactly 1000 tokens (4000 bytes)
	t.Run("1000 tokens", func(t *testing.T) {
		// "# Test\n" = 7 bytes, so body should be 3993 bytes
		body := strings.Repeat("x", 3993)
		sections := []GCISection{
			{Name: "Test", Body: body, Binding: false},
		}
		result, err := AssembleGCIBlock(sections, "")
		if err != nil {
			t.Fatalf("expected no error for 1000 tokens, got %v", err)
		}
		expectedLen := 4000
		if len(result) != expectedLen {
			t.Errorf("expected assembled length %d, got %d", expectedLen, len(result))
		}
		tokens := (len(result) + 3) / 4
		if tokens != 1000 {
			t.Errorf("expected 1000 tokens, got %d", tokens)
		}
	})

	// Test case 2: exactly 1200 tokens (4800 bytes)
	t.Run("1200 tokens", func(t *testing.T) {
		// We want total = 4800 bytes
		// "# Test\n" = 7 bytes, so body should be 4793 bytes
		body := strings.Repeat("x", 4793)
		sections := []GCISection{
			{Name: "Test", Body: body, Binding: false},
		}
		result, err := AssembleGCIBlock(sections, "")
		if err != nil {
			t.Fatalf("expected no error for 1200 tokens, got %v", err)
		}
		expectedLen := 4800
		if len(result) != expectedLen {
			t.Errorf("expected assembled length %d, got %d", expectedLen, len(result))
		}
		tokens := (len(result) + 3) / 4
		if tokens != 1200 {
			t.Errorf("expected 1200 tokens, got %d", tokens)
		}
	})
}

// TestGCIBlockExceedsBudget verifies that assemblies exceeding the 1200 token budget return ErrBudgetExceeded.
func TestGCIBlockExceedsBudget(t *testing.T) {
	// Test case 1: 1201 tokens (4801 bytes)
	t.Run("1201 tokens", func(t *testing.T) {
		// We want total = 4801 bytes
		// "# Test\n" = 7 bytes, so body should be 4794 bytes
		body := strings.Repeat("x", 4794)
		sections := []GCISection{
			{Name: "Test", Body: body, Binding: false},
		}
		result, err := AssembleGCIBlock(sections, "")
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
		if result != "" {
			t.Errorf("expected empty result, got %q", result)
		}
	})

	// Test case 2: 1300 tokens
	t.Run("1300 tokens", func(t *testing.T) {
		// We want total = 5200 bytes (1300 tokens)
		// "# Test\n" = 7 bytes, so body should be 5193 bytes
		body := strings.Repeat("x", 5193)
		sections := []GCISection{
			{Name: "Test", Body: body, Binding: false},
		}
		result, err := AssembleGCIBlock(sections, "")
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded, got %v", err)
		}
		if result != "" {
			t.Errorf("expected empty result, got %q", result)
		}
	})

	// Test case 3: omissions pushing above budget
	t.Run("omissions push over budget", func(t *testing.T) {
		// Create a section that's just under budget (1199 tokens = 4796 bytes)
		// "# Test\n" = 7 bytes, so body should be 4789 bytes
		body := strings.Repeat("x", 4789)
		sections := []GCISection{
			{Name: "Test", Body: body, Binding: false},
		}
		// Add omissions that push it over
		// "# Omissions\n" = 11 bytes, plus omissions content
		// We need to add enough to exceed 4800 bytes total
		// Current: 4796 bytes, need to add at least 5 bytes
		omissions := "short"
		result, err := AssembleGCIBlock(sections, omissions)
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Errorf("expected ErrBudgetExceeded with omissions, got %v", err)
		}
		if result != "" {
			t.Errorf("expected empty result, got %q", result)
		}
	})
}
