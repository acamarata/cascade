package lifecycle

import (
	"strings"
	"testing"
)

// TestRenderUnknownKindReturnsError verifies that Render returns an error
// when called with a kind that has not been registered.
func TestRenderUnknownKindReturnsError(t *testing.T) {
	_, err := Render("unknown_kind", TemplateData{})
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
}

// TestNoEmDashesOrFillerWords renders each registered template with sample data
// and asserts no em-dash characters and no filler words appear in output.
func TestNoEmDashesOrFillerWords(t *testing.T) {
	kinds := []TemplateKind{KindSeeded, KindCreated, KindTicket, KindCrunch}
	data := TemplateData{}

	for _, k := range kinds {
		out, err := Render(k, data)
		if err != nil {
			t.Fatalf("render %s: %v", k, err)
		}
		if strings.Contains(out, "—") {
			t.Errorf("%s contains em-dash", k)
		}
		for _, filler := range []string{"basically", "actually", "just"} {
			if strings.Contains(strings.ToLower(out), filler) {
				t.Errorf("%s contains filler word %q", k, filler)
			}
		}
	}
}
