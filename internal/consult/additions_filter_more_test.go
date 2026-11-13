package consult

import "testing"

// TestFilterAdditionsKeepsInputOrder verifies that additions are returned in the same order
// as the input, in an independent slice that doesn't mutate the original input. It also tests
// edge cases for nil/empty inputs and no passing additions.
func TestFilterAdditionsKeepsInputOrder(t *testing.T) {
	// Test that order is preserved
	adds := []Addition{
		{Text: "A", Tokens: 10, Relevance: 0.9},
		{Text: "B", Tokens: 20, Relevance: 0.8},
		{Text: "C", Tokens: 30, Relevance: 0.7},
	}
	result, err := FilterAdditions(adds, 100, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 additions, got %d", len(result))
	}
	for i, expected := range []string{"A", "B", "C"} {
		if result[i].Text != expected {
			t.Errorf("position %d: expected %s, got %s", i, expected, result[i].Text)
		}
	}

	// Verify that the result is an independent slice (modifying result doesn't affect input)
	result[0].Text = "modified"
	if adds[0].Text == "modified" {
		t.Errorf("modifying result mutated the original input")
	}

	// Call again to verify deterministic behavior
	result2, err := FilterAdditions(adds, 100, 0.5)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if len(result2) != 3 {
		t.Fatalf("expected 3 additions on second call, got %d", len(result2))
	}
	for i, expected := range []string{"A", "B", "C"} {
		if result2[i].Text != expected {
			t.Errorf("second call position %d: expected %s, got %s", i, expected, result2[i].Text)
		}
	}

	// Test nil input
	t.Run("nil input", func(t *testing.T) {
		result, err := FilterAdditions(nil, 100, 0.5)
		if err != nil {
			t.Fatalf("unexpected error for nil input: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil result for nil input, got %v", result)
		}
	})

	// Test empty input
	t.Run("empty input", func(t *testing.T) {
		result, err := FilterAdditions([]Addition{}, 100, 0.5)
		if err != nil {
			t.Fatalf("unexpected error for empty input: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil result for empty input, got %v", result)
		}
	})

	// Test no passing additions (all below threshold)
	t.Run("no passing additions", func(t *testing.T) {
		noPassAdds := []Addition{
			{Text: "low1", Tokens: 10, Relevance: 0.1},
			{Text: "low2", Tokens: 10, Relevance: 0.2},
		}
		result, err := FilterAdditions(noPassAdds, 100, 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil result for no passing additions, got %v", result)
		}
	})

	// Test valid zero relevance
	t.Run("valid zero relevance", func(t *testing.T) {
		zeroRelAdds := []Addition{
			{Text: "zero relevance", Tokens: 10, Relevance: 0.0},
		}
		result, err := FilterAdditions(zeroRelAdds, 100, 0.0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("expected 1 addition for valid zero relevance, got %d", len(result))
		}
	})

	// Test that input text bytes remain unchanged
	t.Run("input bytes unchanged", func(t *testing.T) {
		originalText := "original text"
		byteAdds := []Addition{
			{Text: originalText, Tokens: 10, Relevance: 0.9},
		}
		_, err := FilterAdditions(byteAdds, 100, 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if byteAdds[0].Text != originalText {
			t.Errorf("input text was modified: expected %s, got %s", originalText, byteAdds[0].Text)
		}
	})
}
