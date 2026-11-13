package docket

import (
	"errors"
	"strings"
)

// ErrInvalidSlug is returned when a SlugAnswer contains a blank slug.
var ErrInvalidSlug = errors.New("D2554-invalid-slug")

// SlugAnswer represents a stateless GC slug answer event.
type SlugAnswer struct {
	// Slug is the identifier for this answer.
	Slug string
	// Finished indicates whether processing is complete. The zero value false means not finished.
	Finished bool
}

// ApplySlugAnswer processes a SlugAnswer and returns the resulting wave.
// It is a pure stateless operation with no current-wave parameter or persisted state.
// An invalid (blank) slug returns 0 and ErrInvalidSlug.
// A valid slug with Finished false returns 0 and nil.
func ApplySlugAnswer(ev SlugAnswer) (wave int, err error) {
	if strings.TrimSpace(ev.Slug) == "" {
		return 0, ErrInvalidSlug
	}
	if !ev.Finished {
		return 0, nil
	}
	return 0, nil
}
