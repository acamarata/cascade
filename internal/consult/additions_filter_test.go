package consult

import (
	"errors"
	"math"
	"testing"
)

// TestFilterAdditionsDropsIrrelevant verifies that additions with relevance below the threshold
// are dropped, while those meeting or exceeding the threshold are kept.
func TestFilterAdditionsDropsIrrelevant(t *testing.T) {
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
}

// TestFilterAdditionsInvalidThreshold validates error handling for invalid thresholds.
func TestFilterAdditionsInvalidThreshold(t *testing.T) {
	adds := []Addition{
		{Text: "high relevance", Tokens: 10, Relevance: 0.9},
		{Text: "low relevance", Tokens: 10, Relevance: 0.2},
	}
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
}

// TestFilterAdditionsInvalidAdditions validates that invalid additions are rejected even
// if they would be filtered out.
func TestFilterAdditionsInvalidAdditions(t *testing.T) {
	testCases := []struct {
		name string
		adds []Addition
	}{
		{"invalid blank text", []Addition{{Text: "", Tokens: 10, Relevance: 0.9}}},
		{"invalid negative tokens", []Addition{{Text: "test", Tokens: -5, Relevance: 0.9}}},
		{"invalid relevance NaN", []Addition{{Text: "test", Tokens: 10, Relevance: math.NaN()}}},
		{"invalid relevance out of range", []Addition{{Text: "test", Tokens: 10, Relevance: 1.5}}},
		{"duplicate text", []Addition{
			{Text: "duplicate", Tokens: 10, Relevance: 0.9},
			{Text: "duplicate", Tokens: 20, Relevance: 0.8},
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := FilterAdditions(tc.adds, 100, 0.5)
			if result != nil {
				t.Errorf("expected nil result for %s", tc.name)
			}
			if !errors.Is(err, ErrInvalidAddition) {
				t.Errorf("expected ErrInvalidAddition, got %v", err)
			}
		})
	}
}

// TestFilterAdditionsDropsOverBudget verifies that additions are skipped when they exceed
// the remaining budget, but later additions that fit are still included.
func TestFilterAdditionsDropsOverBudget(t *testing.T) {
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
}

// TestFilterAdditionsBudgetEdges covers exact fits, zero budget and large token values.
func TestFilterAdditionsBudgetEdges(t *testing.T) {
	testCases := []struct {
		name   string
		adds   []Addition
		budget int
		want   int
	}{
		{"exact budget fit", []Addition{
			{Text: "exact", Tokens: 100, Relevance: 0.9},
		}, 100, 1},
		{"zero budget zero tokens", []Addition{
			{Text: "zero cost", Tokens: 0, Relevance: 0.9},
		}, 0, 1},
		{"zero budget non-zero tokens", []Addition{
			{Text: "costs tokens", Tokens: 10, Relevance: 0.9},
		}, 0, 0},
		// First fits, second should also fit (MaxInt - (MaxInt-10) = 10 >= 5)
		{"large tokens safe subtraction", []Addition{
			{Text: "large", Tokens: math.MaxInt - 10, Relevance: 0.9},
			{Text: "small", Tokens: 5, Relevance: 0.9},
		}, math.MaxInt, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := FilterAdditions(tc.adds, tc.budget, 0.5)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != tc.want {
				t.Errorf("%s: expected %d additions, got %d", tc.name, tc.want, len(result))
			}
		})
	}
}
