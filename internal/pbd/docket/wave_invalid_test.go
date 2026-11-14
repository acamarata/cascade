package docket

import (
	"errors"
	"testing"
)

// TestWaveRejectsInvalidSlugAnswer verifies that ApplySlugAnswer rejects
// empty or whitespace-only slugs, including zero-value events and events
// where Finished is true but the slug is blank. Invalid slug takes
// precedence over finished state.
func TestWaveRejectsInvalidSlugAnswer(t *testing.T) {
	tests := []struct {
		name string
		ev   SlugAnswer
	}{
		{"empty slug", SlugAnswer{Slug: ""}},
		{"whitespace slug", SlugAnswer{Slug: "   \t\n "}},
		{"zero value", SlugAnswer{}},
		{"finished with blank slug", SlugAnswer{Slug: " ", Finished: true}},
		{"finished with empty slug", SlugAnswer{Slug: "", Finished: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wave, err := ApplySlugAnswer(tt.ev)
			if wave != 0 {
				t.Errorf("expected wave 0, got %d", wave)
			}
			if !errors.Is(err, ErrInvalidSlug) {
				t.Errorf("expected ErrInvalidSlug, got %v", err)
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != "D2554-invalid-slug" {
				t.Errorf("expected error text 'D2554-invalid-slug', got %q", err.Error())
			}
		})
	}
}
