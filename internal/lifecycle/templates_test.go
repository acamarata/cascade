package lifecycle

import (
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
