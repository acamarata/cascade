package docket

import (
	"errors"
	"regexp"
	"unicode/utf8"
)

// ErrPlainText indicates a validation failure in plain text fields.
var ErrPlainText = errors.New("ERR_PLAIN_TEXT")

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// markdownPattern matches Markdown ATX headings or backtick/tilde fences at
// line start with at most three leading spaces.
var markdownPattern = regexp.MustCompile(`(?m)^[ \t]{0,3}(?:#{1,6}(?:[ \t]+|$)|\x60{3,}|~{3,})`)

// ValidatePlainText checks that the given field and value conform to the
// plain text validation rules. Allowed field names are exactly "title",
// "objective", "acceptance", and "verify", case-sensitive. Unknown or empty
// fields return ErrPlainText. An empty value is valid and denotes absent
// text. Invalid UTF-8, text containing HTML-like tags (matched by the
// exact Go regexp <[^>]*>), and text containing Markdown ATX headings or
// fenced code blocks are rejected with ErrPlainText.
func ValidatePlainText(field, value string) error {
	switch field {
	case "title", "objective", "acceptance", "verify":
	default:
		return ErrPlainText
	}
	if !utf8.ValidString(value) {
		return ErrPlainText
	}
	if htmlTagPattern.MatchString(value) {
		return ErrPlainText
	}
	if markdownPattern.MatchString(value) {
		return ErrPlainText
	}
	return nil
}

// SchemaColumnTypes returns a fresh map of the standard text columns to the
// TEXT schema type. The returned map contains exactly the keys "title",
// "objective", "acceptance", and "verify", each mapped to "TEXT". Mutating
// the returned map does not affect subsequent calls.
func SchemaColumnTypes() map[string]string {
	return map[string]string{
		"title":      "TEXT",
		"objective":  "TEXT",
		"acceptance": "TEXT",
		"verify":     "TEXT",
	}
}
