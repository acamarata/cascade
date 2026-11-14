package docket

import (
	"errors"
	"strings"
)

// ErrInvalidSlug is returned when a SlugAnswer contains a blank slug.
var ErrInvalidSlug = errors.New("D2554-invalid-slug")

// ErrReplaceBlocked is returned when an attempted replacement of a finished ticket is detected.
var ErrReplaceBlocked = errors.New("D2554-replace-blocked")

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
// A valid slug with Finished true returns 0 and ErrReplaceBlocked.
// Invalid Slug takes precedence if Finished is also true.
func ApplySlugAnswer(ev SlugAnswer) (wave int, err error) {
	if strings.TrimSpace(ev.Slug) == "" {
		return 0, ErrInvalidSlug
	}
	if ev.Finished {
		return 0, ErrReplaceBlocked
	}
	return 0, nil
}
