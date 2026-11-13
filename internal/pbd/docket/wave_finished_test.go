package docket

import (
	"errors"
	"testing"
)

// TestWaveBlocksFinishedTicketReplacement verifies that every valid SlugAnswer
// with Finished=true returns 0 and ErrReplaceBlocked, regardless of any
// caller-side prior wave.
func TestWaveBlocksFinishedTicketReplacement(t *testing.T) {
	tests := []struct {
		name string
		ev   SlugAnswer
	}{
		{
			name: "valid slug with finished true",
			ev:   SlugAnswer{Slug: "slug-1", Finished: true},
		},
		{
			name: "valid slug with finished true alternate",
			ev:   SlugAnswer{Slug: "another-slug", Finished: true},
		},
		{
			name: "valid slug no spaces with finished true",
			ev:   SlugAnswer{Slug: "abc123", Finished: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wave, err := ApplySlugAnswer(tt.ev)
			if wave != 0 {
				t.Errorf("ApplySlugAnswer() wave = %d, want 0", wave)
			}
			if err == nil {
				t.Fatal("ApplySlugAnswer() err = nil, want ErrReplaceBlocked")
			}
			if !errors.Is(err, ErrReplaceBlocked) {
				t.Errorf("ApplySlugAnswer() err = %v, want ErrReplaceBlocked", err)
			}
			if err.Error() != "D2554-replace-blocked" {
				t.Errorf("ApplySlugAnswer() err.Error() = %q, want %q", err.Error(), "D2554-replace-blocked")
			}
		})
	}
}
