package docket

import (
	"errors"
	"strings"
)

// ErrScope is returned when a scope entry is invalid.
var ErrScope = errors.New("D2227: invalid scope entry")

// ValidateScopeEntry validates that s is a well-formed path or glob.
// It trims whitespace, strips a trailing parenthesised note, cuts at the
// first " - " separator, and rejects an empty remainder or one containing
// a raw space character.
func ValidateScopeEntry(s string) error {
	s = strings.TrimSpace(s)

	// Strip trailing " (note)"
	if idx := strings.LastIndex(s, " ("); idx >= 0 {
		if strings.HasSuffix(s, ")") {
			s = s[:idx]
		}
	}

	// Cut at first " - "
	if idx := strings.Index(s, " - "); idx >= 0 {
		s = s[:idx]
	}

	s = strings.TrimSpace(s)

	if s == "" || strings.Contains(s, " ") {
		return ErrScope
	}

	return nil
}
