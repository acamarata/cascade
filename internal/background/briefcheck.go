// Package background provides brief validation utilities.
package background

import (
	"encoding/json"
	"errors"
	"strings"
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

	return nil
}

func validateBriefJSON(brief string) error {
	if brief == "" {
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

	allowedKeys := map[string]bool{"scope": true, "apis": true, "verify": true}
	for k := range obj {
		if !allowedKeys[k] {
			return ErrInvalidBrief
		}
	}

	if _, ok := obj["scope"]; !ok {
		return ErrInvalidBrief
	}
	if _, ok := obj["apis"]; !ok {
		return ErrInvalidBrief
	}
	if _, ok := obj["verify"]; !ok {
		return ErrInvalidBrief
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

	return nil
}
