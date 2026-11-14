package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestDecideReturnsYesFromRule verifies that a matching rule returning YES
// yields YES with confidence 1.0 and the correct ID.
func TestDecideReturnsYesFromRule(t *testing.T) {
	kind := "file-type"
	text := "a_test.go"
	expectedID := computeExpectedID(kind, text)

	rule1 := func(_ Question) (string, bool) {
		return YES, true
	}
	rule2Called := false
	rule2 := func(_ Question) (string, bool) {
		rule2Called = true
		return NO, true
	}

	q := Question{
		Kind:  kind,
		Text:  text,
		Rules: []Rule{rule1, rule2},
	}

	v, err := Decide(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Answer != YES {
		t.Errorf("expected Answer=%q, got %q", YES, v.Answer)
	}
	if v.Confidence != 1.0 {
		t.Errorf("expected Confidence=1.0, got %v", v.Confidence)
	}
	if v.ID != expectedID {
		t.Errorf("expected ID=%q, got %q", expectedID, v.ID)
	}
	if rule2Called {
		t.Error("later rule was invoked after first match")
	}
}

// TestDecideReturnsNoFromRule verifies that a matching rule returning NO
// yields NO with confidence 1.0, and that changing rules preserves ID
// when Kind/Text are unchanged.
func TestDecideReturnsNoFromRule(t *testing.T) {
	kind := "file-type"
	text := "main.go"
	expectedID := computeExpectedID(kind, text)

	rule := func(_ Question) (string, bool) {
		return NO, true
	}

	q := Question{
		Kind:  kind,
		Text:  text,
		Rules: []Rule{rule},
	}

	v, err := Decide(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Answer != NO {
		t.Errorf("expected Answer=%q, got %q", NO, v.Answer)
	}
	if v.Confidence != 1.0 {
		t.Errorf("expected Confidence=1.0, got %v", v.Confidence)
	}
	if v.ID != expectedID {
		t.Errorf("expected ID=%q, got %q", expectedID, v.ID)
	}

	// Verify ID is preserved when rules change but Kind/Text stay the same
	rule2 := func(_ Question) (string, bool) {
		return YES, true
	}
	q2 := Question{
		Kind:  kind,
		Text:  text,
		Rules: []Rule{rule2},
	}
	v2, err := Decide(q2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v2.ID != expectedID {
		t.Errorf("ID should be preserved when Kind/Text unchanged, got %q", v2.ID)
	}
}

// TestDecideNoRuleMatchEscalates verifies that nil, empty, or entirely
// unmatched Rules returns ESCALATE with confidence 0.0 and the correct ID.
// rejecting returns a rule that never matches and proposes answer.
func rejecting(answer string) Rule {
	return func(_ Question) (string, bool) { return answer, false }
}

// noMatchRuleSets are rule lists in which no rule matches.
func noMatchRuleSets() map[string][]Rule {
	return map[string][]Rule{
		"nil rules":                             nil,
		"empty rules":                           {},
		"unmatched rules":                       {rejecting(""), rejecting("")},
		"unmatched with invalid answer strings": {rejecting("INVALID"), rejecting("MAYBE")},
	}
}

func TestDecideNoRuleMatchEscalates(t *testing.T) {
	kind, text := "file-type", "unknown"
	expectedID := computeExpectedID(kind, text)
	for name, rules := range noMatchRuleSets() {
		t.Run(name, func(t *testing.T) {
			v, err := Decide(Question{Kind: kind, Text: text, Rules: rules})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Answer != ESCALATE {
				t.Errorf("expected Answer=%q, got %q", ESCALATE, v.Answer)
			}
			if v.Confidence != 0.0 {
				t.Errorf("expected Confidence=0.0, got %v", v.Confidence)
			}
			if v.ID != expectedID {
				t.Errorf("expected ID=%q, got %q", expectedID, v.ID)
			}
		})
	}
}

// computeExpectedID computes the expected ID for testing.
func computeExpectedID(kind, text string) string {
	data := []byte(kind + "\x00" + text)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
