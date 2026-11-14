package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func expectedID(kind, text string) string {
	data := []byte(kind + "\x00" + text)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestDecideReturnsYesFromRule verifies that a YES answer from the first matching rule is returned.
func TestDecideReturnsYesFromRule(t *testing.T) {
	called := false
	rules := []Rule{
		func(q Question) (string, bool) {
			return YES, true
		},
		func(q Question) (string, bool) {
			called = true
			return NO, true
		},
	}
	q := Question{Kind: "file-type", Text: "a_test.go", Rules: rules}
	v, err := Decide(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Answer != YES {
		t.Errorf("expected YES, got %s", v.Answer)
	}
	if v.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %f", v.Confidence)
	}
	wantID := expectedID("file-type", "a_test.go")
	if v.ID != wantID {
		t.Errorf("expected ID %s, got %s", wantID, v.ID)
	}
	if called {
		t.Error("later rule should not have been invoked")
	}

	// matched ESCALATE also has confidence 1.0
	escRules := []Rule{
		func(q Question) (string, bool) {
			return ESCALATE, true
		},
	}
	q2 := Question{Kind: "file-type", Text: "a_test.go", Rules: escRules}
	v2, err := Decide(q2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v2.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0 for ESCALATE, got %f", v2.Confidence)
	}
}

// TestDecideReturnsNoFromRule verifies that a NO answer from a matching rule is returned.
func TestDecideReturnsNoFromRule(t *testing.T) {
	rules := []Rule{
		func(q Question) (string, bool) {
			return NO, true
		},
	}
	q := Question{Kind: "file-type", Text: "main.go", Rules: rules}
	v, err := Decide(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Answer != NO {
		t.Errorf("expected NO, got %s", v.Answer)
	}
	if v.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %f", v.Confidence)
	}
	wantID := expectedID("file-type", "main.go")
	if v.ID != wantID {
		t.Errorf("expected ID %s, got %s", wantID, v.ID)
	}

	// changing rules/answer while preserving Kind/Text preserves ID
	rules2 := []Rule{
		func(q Question) (string, bool) {
			return YES, true
		},
	}
	q2 := Question{Kind: "file-type", Text: "main.go", Rules: rules2}
	v2, err := Decide(q2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v2.ID != wantID {
		t.Errorf("expected same ID %s, got %s", wantID, v2.ID)
	}

	// changing Kind/Text recomputes digest
	q3 := Question{Kind: "other-type", Text: "main.go", Rules: rules}
	v3, err := Decide(q3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v3.ID == wantID {
		t.Error("expected different ID when Kind changes")
	}
}

// TestDecideNoRuleMatchEscalates verifies that no match returns ESCALATE with confidence 0.0.
func TestDecideNoRuleMatchEscalates(t *testing.T) {
	wantID := expectedID("file-type", "unknown")

	cases := []struct {
		name  string
		rules []Rule
	}{
		{"nil rules", nil},
		{"empty rules", []Rule{}},
		{"unmatched rules", []Rule{
			func(q Question) (string, bool) { return YES, false },
		}},
		{"unmatched invalid answer", []Rule{
			func(q Question) (string, bool) { return "INVALID", false },
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := Question{Kind: "file-type", Text: "unknown", Rules: tc.rules}
			v, err := Decide(q)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Answer != ESCALATE {
				t.Errorf("expected ESCALATE, got %s", v.Answer)
			}
			if v.Confidence != 0.0 {
				t.Errorf("expected confidence 0.0, got %f", v.Confidence)
			}
			if v.ID != wantID {
				t.Errorf("expected ID %s, got %s", wantID, v.ID)
			}
		})
	}
}
