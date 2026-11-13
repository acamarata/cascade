// Package docket provides message row validation for the docket system.
package docket

import "errors"

// MessageRow represents a single row of message metadata as key-value pairs.
type MessageRow map[string]any

// ErrMessageMetadata is returned when column validation fails.
var ErrMessageMetadata = errors.New("D5903")

// ValidateMessageRowMetadata validates that columns contain exactly the required
// metadata keys (folder, state, time, label) with no duplicates or extras.
// A nil row triggers schema-only validation, returning nil, nil on success.
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

	return row, nil
}
