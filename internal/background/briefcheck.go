// Package background provides brief validation utilities.
package background

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrBriefTooLarge is returned when the brief byte length is 4096 or greater.
var ErrBriefTooLarge = errors.New("brief_too_large")

// ErrMissingScope is returned when the scope field is missing or empty.
var ErrMissingScope = errors.New("missing_scope")

// ErrInvalidBrief is returned when the brief is malformed, has invalid JSON,
// unknown keys, duplicate keys, wrong types, or invalid UTF-8.
var ErrInvalidBrief = errors.New("invalid_brief")

// CheckBrief validates a brief string and optionally runs a verification callback.
// It returns sentinel errors for size or structural violations without invoking the callback.
func CheckBrief(brief string, verifyPassesOnBase func(brief string) bool) error {
	if len(brief) >= 4096 {
		return ErrBriefTooLarge
	}

	if err := validateBriefJSON(brief); err != nil {
		return err
	}

	if verifyPassesOnBase != nil {
		verifyPassesOnBase(brief)
	}

	return nil
}

// checkBriefKeys rejects unknown keys and requires scope, apis and verify.
func checkBriefKeys(obj map[string]json.RawMessage) error {
	allowedKeys := map[string]bool{"scope": true, "apis": true, "verify": true}
	for k := range obj {
		if !allowedKeys[k] {
			return ErrInvalidBrief
		}
	}

	if _, ok := obj["scope"]; !ok {
		return ErrMissingScope
	}
	for k := range allowedKeys {
		if _, ok := obj[k]; !ok {
			return ErrInvalidBrief
		}
	}

	return nil
}

func validateBriefJSON(brief string) error {
	if brief == "" {
		return ErrInvalidBrief
	}

	if !utf8.ValidString(brief) {
		return ErrInvalidBrief
	}

	if hasDuplicateKeys(brief) {
		return ErrInvalidBrief
	}

	dec := json.NewDecoder(strings.NewReader(brief))
	dec.DisallowUnknownFields()

	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return ErrInvalidBrief
	}

	if dec.More() {
		return ErrInvalidBrief
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ErrInvalidBrief
	}

	if err := checkBriefKeys(obj); err != nil {
		return err
	}

	var scope []string
	if err := json.Unmarshal(obj["scope"], &scope); err != nil {
		return ErrInvalidBrief
	}

	var apis []string
	if err := json.Unmarshal(obj["apis"], &apis); err != nil {
		return ErrInvalidBrief
	}

	var verify string
	if err := json.Unmarshal(obj["verify"], &verify); err != nil {
		return ErrInvalidBrief
	}

	if err := validateScope(scope); err != nil {
		return err
	}

	return nil
}

// validateScope checks that every entry is a valid repo-relative slash path.
// Entries must be file paths with no directory/glob, absolute/backslash/dot/dotdot/empty
// components, NUL or whitespace characters, and the final component must contain a dot.
// Returns ErrMissingScope when the slice is effectively empty, or ErrInvalidBrief
// for malformed, unsafe or duplicate entries.
func validateScope(scope []string) error {
	if len(scope) == 0 {
		return ErrMissingScope
	}

	seen := make(map[string]bool)
	for _, s := range scope {
		if strings.TrimSpace(s) == "" {
			return ErrMissingScope
		}

		if !utf8.ValidString(s) {
			return ErrInvalidBrief
		}

		if strings.ContainsRune(s, '\x00') {
			return ErrInvalidBrief
		}

		if strings.ContainsAny(s, " \t\n\r") {
			return ErrInvalidBrief
		}

		if strings.ContainsRune(s, '\\') {
			return ErrInvalidBrief
		}

		if strings.ContainsRune(s, '*') || strings.ContainsRune(s, '?') {
			return ErrInvalidBrief
		}

		if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
			return ErrInvalidBrief
		}

		parts := strings.Split(s, "/")
		for _, p := range parts {
			if p == "" || p == "." || p == ".." {
				return ErrInvalidBrief
			}
		}

		last := parts[len(parts)-1]
		if !strings.Contains(last, ".") {
			return ErrInvalidBrief
		}

		if seen[s] {
			return ErrInvalidBrief
		}
		seen[s] = true
	}

	return nil
}

// hasDuplicateKeys reports whether the top-level JSON object in brief contains duplicate keys.
func hasDuplicateKeys(brief string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(brief), &obj); err != nil {
		return false
	}

	dec := json.NewDecoder(strings.NewReader(brief))
	t, err := dec.Token()
	if err != nil {
		return false
	}
	if delim, ok := t.(json.Delim); !ok || delim != '{' {
		return false
	}

	count := 0
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		if _, ok := t.(string); !ok {
			return false
		}
		count++

		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return false
		}
	}

	return count != len(obj)
}
