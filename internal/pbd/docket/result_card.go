// Package docket provides result card types and validation.
package docket

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrCardEmpty is returned when CommitInfo.Files is nil or empty.
var ErrCardEmpty = errors.New("CARD_EMPTY")

// ErrCardInput is returned when CommitInfo contains invalid entries.
var ErrCardInput = errors.New("CARD_INVALID")

// Cap is the maximum allowed byte size for rendered output.
const Cap = 4096

// CommitInfo holds the file paths and declaration texts for a result card.
type CommitInfo struct {
	// Files lists the file paths associated with the commit.
	Files []string
	// Decls lists the declaration texts associated with the commit.
	Decls []string
}

// Render validates the CommitInfo and returns the rendered card text.
// It returns ("", ErrCardEmpty) if Files is nil or empty.
// It returns ("", ErrCardInput) if any entry is empty, not valid UTF-8,
// contains CR, LF, or NUL bytes, or is duplicated within its own list.
func Render(c CommitInfo) (string, error) {
	if len(c.Files) == 0 {
		return "", ErrCardEmpty
	}

	if err := validateList(c.Files); err != nil {
		return "", err
	}
	if err := validateList(c.Decls); err != nil {
		return "", err
	}

	var b strings.Builder
	for _, f := range c.Files {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	for _, d := range c.Decls {
		b.WriteString(d)
		b.WriteByte('\n')
	}

	out := b.String()
	if len(out) > Cap {
		return "", ErrCardInput
	}

	return out, nil
}

// BuildResultCard formats a CommitInfo into a human-readable result card.
// It renders "files:\n" followed by "- " and each file, then "decls:\n"
// followed by "- " and each declaration. An empty Decls slice produces
// the header with no entries. Returns ErrCardEmpty if Files is empty,
// ErrCardInput if validation fails or the output exceeds Cap.
func BuildResultCard(c CommitInfo) (string, error) {
	if len(c.Files) == 0 {
		return "", ErrCardEmpty
	}

	if err := validateList(c.Files); err != nil {
		return "", err
	}
	if err := validateList(c.Decls); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("files:\n")
	for _, f := range c.Files {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteByte('\n')
	}
	b.WriteString("decls:\n")
	for _, d := range c.Decls {
		b.WriteString("- ")
		b.WriteString(d)
		b.WriteByte('\n')
	}

	out := b.String()
	if len(out) > Cap {
		return "", ErrCardInput
	}

	return out, nil
}

func validateList(list []string) error {
	seen := make(map[string]struct{}, len(list))
	for _, s := range list {
		if s == "" {
			return ErrCardInput
		}
		if !utf8.ValidString(s) {
			return ErrCardInput
		}
		if strings.ContainsAny(s, "\r\n\x00") {
			return ErrCardInput
		}
		if _, ok := seen[s]; ok {
			return ErrCardInput
		}
		seen[s] = struct{}{}
	}
	return nil
}
