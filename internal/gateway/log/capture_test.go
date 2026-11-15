package log

import (
	"strings"
	"testing"
)

func TestScrubFormsRejectsSpecialChars(t *testing.T) {
	invalidKeys := []string{
		"key\"quote",
		"key\\backslash",
		"key\x01control",
		"key\x1fcontrol",
		"key\x7fcontrol",
		"key\nnewline",
		"key\ttab",
	}

	for _, key := range invalidKeys {
		_, err := ScrubForms(key)
		if err == nil {
			t.Errorf("ScrubForms(%q) expected error, got nil", key)
		}
	}
}

func TestScrubFormsValid(t *testing.T) {
	sk, err := ScrubForms("abc")
	if err != nil {
		t.Fatalf("ScrubForms(\"abc\") unexpected error: %v", err)
	}

	// Four forms should be generated
	expectedRaw := "[abc]"
	expectedGoQuoted := `"[abc]"`
	expectedJSON := `"[abc]"`
	expectedNoHTML := `"[abc]"`

	forms := []string{sk.Raw, sk.GoQuoted, sk.JSON, sk.NoHTMLEscape}

	// Check all expected values exist in forms
	expected := map[string]bool{
		expectedRaw:      false,
		expectedGoQuoted: false,
		expectedJSON:     false,
		expectedNoHTML:   false,
	}
	for _, f := range forms {
		if _, ok := expected[f]; ok {
			expected[f] = true
		} else {
			t.Errorf("unexpected form value: %q", f)
		}
	}
	for v, found := range expected {
		if !found {
			t.Errorf("expected form %q not found", v)
		}
	}
}

func TestScrubFormsLongestFirst(t *testing.T) {
	sk, err := ScrubForms("mykey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	forms := []string{sk.Raw, sk.GoQuoted, sk.JSON, sk.NoHTMLEscape}

	// Verify ordering: longest first
	for i := 0; i < len(forms)-1; i++ {
		if len(forms[i]) < len(forms[i+1]) {
			t.Errorf("forms not ordered longest first at index %d: lengths %v, values %q",
				i, []int{len(forms[0]), len(forms[1]), len(forms[2]), len(forms[3])}, forms)
		}
	}
}

func TestScrubFormsNoFragmentRemains(t *testing.T) {
	key := "secret"
	sk, err := ScrubForms(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	replacement := "[" + key + "]"
	forms := []string{sk.Raw, sk.GoQuoted, sk.JSON, sk.NoHTMLEscape}
	for _, f := range forms {
		// Every form should contain the replacement pattern [key]
		if !strings.Contains(f, replacement) {
			t.Errorf("form %q does not contain replacement %s", f, replacement)
		}
		// No form should contain the bare key outside the replacement brackets.
		// Strip all occurrences of the replacement and check for residual key.
		stripped := strings.ReplaceAll(f, replacement, "")
		if strings.Contains(stripped, key) {
			t.Errorf("form %q still contains the original key %q outside replacement", f, key)
		}
	}
}
