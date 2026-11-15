// Package atlas provides full-text search utilities for the notes system.
package atlas

import (
	"errors"
	"regexp"
	"strings"
)

// Sentinel errors for FTS body operations.
var (
	// ErrBodyInput indicates invalid body input was provided.
	ErrBodyInput = errors.New("invalid body input")

	// ErrBodyNotFound indicates the requested note was not found.
	ErrBodyNotFound = errors.New("note not found")

	// ErrLegacyBody indicates a legacy body column reference was detected (D4045).
	ErrLegacyBody = errors.New("D4045: legacy body column reference")

	// ErrFTSBodyPolicyRequired indicates that an atlas body home policy is required.
	ErrFTSBodyPolicyRequired = errors.New("atlas: body home policy required")

	// ErrFTSBodyProhibited indicates that full body storage is prohibited.
	ErrFTSBodyProhibited = errors.New("atlas: full body storage prohibited")
)

// CheckLegacyBody checks if the given SQL text contains references to the legacy
// body column in the notes table. It returns ErrBodyInput for blank input,
// ErrLegacyBody if a legacy body reference is detected, or nil otherwise.
//
// Recognized patterns (case-insensitive):
//   - SELECT body FROM notes
//   - SELECT notes.body FROM ...
//   - INSERT INTO notes ... with body column
//   - UPDATE notes SET body = ...
//
// Allowed:
//   - notes_fts.body references
//   - String literals containing "body"
//   - Comments containing "body"
func CheckLegacyBody(sqlText string) error {
	if strings.TrimSpace(sqlText) == "" {
		return ErrBodyInput
	}

	// Remove single-line comments (-- ...)
	commentPattern := regexp.MustCompile(`--[^\n]*`)
	cleaned := commentPattern.ReplaceAllString(sqlText, "")

	// Remove multi-line comments (/* ... */)
	multiCommentPattern := regexp.MustCompile(`/\*[^*]*\*+(?:[^/*][^*]*\*+)*/`)
	cleaned = multiCommentPattern.ReplaceAllString(cleaned, "")

	// Remove string literals (single-quoted strings)
	stringLiteralPattern := regexp.MustCompile(`'[^']*'`)
	cleaned = stringLiteralPattern.ReplaceAllString(cleaned, "")

	// Remove double-quoted identifiers temporarily to check context
	// We need to preserve them for pattern matching but handle them specially

	// Convert to lowercase for case-insensitive matching
	lower := strings.ToLower(cleaned)

	// Check for notes_fts.body - this is allowed
	ftsPattern := regexp.MustCompile(`notes_fts\.body|"notes_fts"\."body"`)
	if ftsPattern.MatchString(lower) {
		// Replace allowed patterns before checking for violations
		allowedPattern := regexp.MustCompile(`(?i)notes_fts\.body|"notes_fts"\."body"`)
		cleaned = allowedPattern.ReplaceAllString(cleaned, "")
		lower = strings.ToLower(cleaned)
	}

	// Pattern 1: SELECT body FROM notes (with optional qualifiers and whitespace)
	selectPattern := regexp.MustCompile(`(?i)\bselect\b.*?\b(?:"body"|body)\b.*?\bfrom\b.*?\b(?:"notes"|notes)\b`)
	if selectPattern.MatchString(lower) {
		return ErrLegacyBody
	}

	// Pattern 2: SELECT notes.body FROM ... (qualified column reference)
	qualifiedPattern := regexp.MustCompile(`(?i)\bselect\b.*?\b(?:"notes"|notes)\.(?:"body"|body)\b`)
	if qualifiedPattern.MatchString(lower) {
		return ErrLegacyBody
	}

	// Pattern 3: INSERT INTO notes ... with body column
	insertPattern := regexp.MustCompile(`(?i)\binsert\b.*?\binto\b.*?\b(?:"notes"|notes)\b.*?\b(?:"body"|body)\b`)
	if insertPattern.MatchString(lower) {
		return ErrLegacyBody
	}

	// Pattern 4: UPDATE notes SET body = ...
	updatePattern := regexp.MustCompile(`(?i)\bupdate\b.*?\b(?:"notes"|notes)\b.*?\bset\b.*?\b(?:"body"|body)\b\s*=`)
	if updatePattern.MatchString(lower) {
		return ErrLegacyBody
	}

	return nil
}
