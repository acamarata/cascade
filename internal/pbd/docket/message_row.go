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
	required := map[string]bool{
		"folder": false,
		"state":  false,
		"time":   false,
		"label":  false,
	}

	if len(columns) != 4 {
		return nil, ErrMessageMetadata
	}

	for _, col := range columns {
		if col == "" {
			return nil, ErrMessageMetadata
		}
		found, ok := required[col]
		if !ok || found {
			return nil, ErrMessageMetadata
		}
		required[col] = true
	}

	for _, v := range required {
		if !v {
			return nil, ErrMessageMetadata
		}
	}

	if row == nil {
		return nil, nil
	}

	if len(row) != 4 {
		return nil, ErrMessageMetadata
	}

	for k := range row {
		if !required[k] {
			return nil, ErrMessageMetadata
		}
	}

	// Validate folder
	folderVal, ok := row["folder"]
	if !ok {
		return nil, ErrMessageMetadata
	}
	folder, ok := folderVal.(string)
	if !ok || strings.TrimSpace(folder) == "" {
		return nil, ErrMessageMetadata
	}

	// Validate state
	stateVal, ok := row["state"]
	if !ok {
		return nil, ErrMessageMetadata
	}
	state, ok := stateVal.(string)
	if !ok || strings.TrimSpace(state) == "" {
		return nil, ErrMessageMetadata
	}

	// Validate time
	timeVal, ok := row["time"]
	if !ok {
		return nil, ErrMessageMetadata
	}
	t, ok := timeVal.(time.Time)
	if !ok || t.IsZero() {
		return nil, ErrMessageMetadata
	}

	// Validate label
	labelVal, ok := row["label"]
	if !ok {
		return nil, ErrMessageMetadata
	}
	label, ok := labelVal.(string)
	if !ok {
		return nil, ErrMessageMetadata
	}
	if !utf8.ValidString(label) {
		return nil, ErrMessageMetadata
	}
	if len(label) < 1 || len(label) > 64 {
		return nil, ErrMessageMetadata
	}
	if strings.ContainsAny(label, "/\\") {
		return nil, ErrMessageMetadata
	}
	if label == "." || label == ".." {
		return nil, ErrMessageMetadata
	}
	if strings.ContainsAny(label, "\r\n\x00") {
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
