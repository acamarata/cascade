package consult

import (
	"errors"
	"math"
	"testing"
)

// TestFilterAdditionsDropsIrrelevant verifies that additions with relevance below the threshold
// are dropped, while those meeting or exceeding the threshold are kept. It also validates
// error handling for invalid thresholds and invalid additions.
func TestFilterAdditionsDropsIrrelevant(t *testing.T) {
	// Test basic relevance filtering with threshold 0.5
	adds := []Addition{
		{Text: "high relevance", Tokens: 10, Relevance: 0.9},
		{Text: "low relevance", Tokens: 10, Relevance: 0.2},
	}
	result, err := FilterAdditions(adds, 100, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 addition, got %d", len(result))
	}
	if result[0].Text != "high relevance" {
		t.Errorf("expected 'high relevance', got %s", result[0].Text)
	}

	// Test that equality qualifies (relevance == threshold is kept)
	addsEqual := []Addition{
		{Text: "exact match", Tokens: 10, Relevance: 0.5},
	}
	resultEqual, err := FilterAdditions(addsEqual, 100, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resultEqual) != 1 {
		t.Errorf("expected 1 addition for equality, got %d", len(resultEqual))
	}

	// Test invalid thresholds
	testCases := []struct {
		name      string
		threshold float64
	}{
		{"NaN threshold", math.NaN()},
		{"positive infinity threshold", math.Inf(1)},
		{"negative infinity threshold", math.Inf(-1)},
		{"threshold above 1", 1.1},
		{"threshold below 0", -0.1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := FilterAdditions(adds, 100, tc.threshold)
			if result != nil {
				t.Errorf("expected nil result for invalid threshold")
			}
			if !errors.Is(err, ErrInvalidRelevance) {
				t.Errorf("expected ErrInvalidRelevance, got %v", err)
			}
		})
	}

	// Test invalid additions even if they would be filtered out
	t.Run("invalid blank text", func(t *testing.T) {
		invalidAdds := []Addition{
			{Text: "", Tokens: 10, Relevance: 0.9},
		}
		result, err := FilterAdditions(invalidAdds, 100, 0.5)
		if result != nil {
			t.Errorf("expected nil result for invalid addition")
		}
		if !errors.Is(err, ErrInvalidAddition) {
			t.Errorf("expected ErrInvalidAddition, got %v", err)
		}
	})

	t.Run("invalid negative tokens", func(t *testing.T) {
		invalidAdds := []Addition{
			{Text: "test", Tokens: -5, Relevance: 0.9},
		}
		result, err := FilterAdditions(invalidAdds, 100, 0.5)
		if result != nil {
			t.Errorf("expected nil result for invalid addition")
		}
		if !errors.Is(err, ErrInvalidAddition) {
			t.Errorf("expected ErrInvalidAddition, got %v", err)
		}
	})

	t.Run("invalid relevance NaN", func(t *testing.T) {
		invalidAdds := []Addition{
			{Text: "test", Tokens: 10, Relevance: math.NaN()},
		}
		result, err := FilterAdditions(invalidAdds, 100, 0.5)
		if result != nil {
			t.Errorf("expected nil result for invalid addition")
		}
		if !errors.Is(err, ErrInvalidAddition) {
			t.Errorf("expected ErrInvalidAddition, got %v", err)
		}
	})

	t.Run("invalid relevance out of range", func(t *testing.T) {
		invalidAdds := []Addition{
			{Text: "test", Tokens: 10, Relevance: 1.5},
		}
		result, err := FilterAdditions(invalidAdds, 100, 0.5)
		if result != nil {
			t.Errorf("expected nil result for invalid addition")
		}
		if !errors.Is(err, ErrInvalidAddition) {
			t.Errorf("expected ErrInvalidAddition, got %v", err)
		}
	})

	t.Run("duplicate text", func(t *testing.T) {
		dupAdds := []Addition{
			{Text: "duplicate", Tokens: 10, Relevance: 0.9},
			{Text: "duplicate", Tokens: 20, Relevance: 0.8},
		}
		result, err := FilterAdditions(dupAdds, 100, 0.5)
		if result != nil {
			t.Errorf("expected nil result for duplicate text")
		}
		if !errors.Is(err, ErrInvalidAddition) {
			t.Errorf("expected ErrInvalidAddition, got %v", err)
		}
	})
}

// TestFilterAdditionsDropsOverBudget verifies that additions are skipped when they exceed
// the remaining budget, but later additions that fit are still included. It also tests
// edge cases around exact budget fits and zero budget.
func TestFilterAdditionsDropsOverBudget(t *testing.T) {
	// Test budget filtering with skipping oversized additions
	adds := []Addition{
		{Text: "first", Tokens: 60, Relevance: 0.9},
		{Text: "second", Tokens: 50, Relevance: 0.9},
		{Text: "third", Tokens: 30, Relevance: 0.9},
	}
	result, err := FilterAdditions(adds, 100, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 additions, got %d", len(result))
	}
	if result[0].Text != "first" || result[0].Tokens != 60 {
		t.Errorf("expected first addition to be kept")
	}
	if result[1].Text != "third" || result[1].Tokens != 30 {
		t.Errorf("expected third addition to be kept after skipping second")
	}

	// Test exact budget fit
	t.Run("exact budget fit", func(t *testing.T) {
		exactAdds := []Addition{
			{Text: "exact", Tokens: 100, Relevance: 0.9},
		}
		result, err := FilterAdditions(exactAdds, 100, 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("expected 1 addition for exact budget fit, got %d", len(result))
		}
	})

	// Test zero budget with zero-token entries
	t.Run("zero budget zero tokens", func(t *testing.T) {
		zeroAdds := []Addition{
			{Text: "zero cost", Tokens: 0, Relevance: 0.9},
		}
		result, err := FilterAdditions(zeroAdds, 0, 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("expected 1 addition for zero budget with zero tokens, got %d", len(result))
		}
	})

	// Test zero budget with non-zero tokens
	t.Run("zero budget non-zero tokens", func(t *testing.T) {
		nonZeroAdds := []Addition{
			{Text: "costs tokens", Tokens: 10, Relevance: 0.9},
		}
		result, err := FilterAdditions(nonZeroAdds, 0, 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("expected 0 additions for zero budget with non-zero tokens, got %d", len(result))
		}
	})

	// Test maximum int handling
	t.Run("large tokens safe subtraction", func(t *testing.T) {
		largeAdds := []Addition{
			{Text: "large", Tokens: math.MaxInt - 10, Relevance: 0.9},
			{Text: "small", Tokens: 5, Relevance: 0.9},
		}
		result, err := FilterAdditions(largeAdds, math.MaxInt, 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// First fits, second should also fit (MaxInt - (MaxInt-10) = 10 >= 5)
		if len(result) != 2 {
			t.Errorf("expected 2 additions with safe large token values, got %d", len(result))
		}
	})
}
