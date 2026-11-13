package consult

import "testing"

// assertOrderABC checks that result holds the texts A, B, C in that order.
func assertOrderABC(t *testing.T, label string, result []Addition) {
	t.Helper()
	if len(result) != 3 {
		t.Fatalf("%sexpected 3 additions, got %d", label, len(result))
	}
	for i, expected := range []string{"A", "B", "C"} {
		if result[i].Text != expected {
			t.Errorf("%sposition %d: expected %s, got %s", label, i, expected, result[i].Text)
		}
	}
}

// TestFilterAdditionsKeepsInputOrder verifies that additions are returned in the same order
// as the input, in an independent slice that doesn't mutate the original input.
func TestFilterAdditionsKeepsInputOrder(t *testing.T) {
	adds := []Addition{
		{Text: "A", Tokens: 10, Relevance: 0.9},
		{Text: "B", Tokens: 20, Relevance: 0.8},
		{Text: "C", Tokens: 30, Relevance: 0.7},
	}
	result, err := FilterAdditions(adds, 100, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertOrderABC(t, "", result)

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
	assertOrderABC(t, "second call: ", result2)
}

// TestFilterAdditionsEmptyAndNoPassing covers nil, empty and nothing-passes inputs.
func TestFilterAdditionsEmptyAndNoPassing(t *testing.T) {
	cases := []struct {
		name string
		adds []Addition
	}{
		{"nil input", nil},
		{"empty input", []Addition{}},
		{"no passing additions", []Addition{
			{Text: "low1", Tokens: 10, Relevance: 0.1},
			{Text: "low2", Tokens: 10, Relevance: 0.2},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := FilterAdditions(tc.adds, 100, 0.5)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != nil {
				t.Errorf("expected nil result for %s, got %v", tc.name, result)
			}
		})
	}
}

// TestFilterAdditionsZeroRelevanceAndInputUnchanged covers a valid zero relevance and
// that the input text is left untouched.
func TestFilterAdditionsZeroRelevanceAndInputUnchanged(t *testing.T) {
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
