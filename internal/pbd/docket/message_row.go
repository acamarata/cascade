// Package docket provides message row validation for the docket system.
package docket

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// MessageRow represents a single row of message metadata as key-value pairs.
type MessageRow map[string]any

// ErrMessageMetadata is returned when row validation fails.
var ErrMessageMetadata = errors.New("D5903")

// ValidateMessageRowMetadata validates that columns contain exactly the required
// metadata keys (folder, state, time, label) and that row contains exactly those
// keys with valid values. Returns a fresh map with the original values on success.
// A nil row is valid and returns nil, nil.
func ValidateMessageRowMetadata(columns []string, row MessageRow) (MessageRow, error) {
	if !validMessageColumns(columns) {
		return nil, ErrMessageMetadata
	}

	if row == nil {
		return nil, nil
	}

	if len(row) != 4 {
		return nil, ErrMessageMetadata
	}

	folder, ok := messageRowText(row, "folder")
	if !ok {
		return nil, ErrMessageMetadata
	}
	state, ok := messageRowText(row, "state")
	if !ok {
		return nil, ErrMessageMetadata
	}
	t, ok := row["time"].(time.Time)
	if !ok || t.IsZero() {
		return nil, ErrMessageMetadata
	}
	label, ok := row["label"].(string)
	if !ok || !validMessageLabel(label) {
		return nil, ErrMessageMetadata
	}

	// Return fresh map
	fresh := make(MessageRow, 4)
	fresh["folder"] = folder
	fresh["state"] = state
	fresh["time"] = t
	fresh["label"] = label

	return fresh, nil
}

// validMessageColumns reports whether columns are exactly the four required
// keys, each non-empty and appearing once.
func validMessageColumns(columns []string) bool {
	if len(columns) != 4 {
		return false
	}

	required := map[string]bool{
		"folder": false,
		"state":  false,
		"time":   false,
		"label":  false,
	}

	for _, col := range columns {
		found, ok := required[col]
		if col == "" || !ok || found {
			return false
		}
		required[col] = true
	}

	for _, v := range required {
		if !v {
			return false
		}
	}
	return true
}

// messageRowText returns row[key] as a string that is not blank.
func messageRowText(row MessageRow, key string) (string, bool) {
	v, ok := row[key].(string)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// validMessageLabel reports whether label is a safe 1-64 byte label.
func validMessageLabel(label string) bool {
	if !utf8.ValidString(label) {
		return false
	}
	if len(label) < 1 || len(label) > 64 {
		return false
	}
	if strings.ContainsAny(label, "/\\") {
		return false
	}
	if label == "." || label == ".." {
		return false
	}
	return !strings.ContainsAny(label, "\r\n\x00")
}
