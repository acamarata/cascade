package docket

import (
	"testing"
	"time"
)

func TestMessageRowInsertRejectsBodyField(t *testing.T) {
	validTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		row  MessageRow
	}{
		{
			name: "body field lowercase",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "test",
				"body":   "message content",
			},
		},
		{
			name: "Body field capitalized",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "test",
				"Body":   "message content",
			},
		},
		{
			name: "BODY field uppercase",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "test",
				"BODY":   "message content",
			},
		},
		{
			name: "extra unknown field",
			row: MessageRow{
				"folder":  "inbox",
				"state":   "pending",
				"time":    validTime,
				"label":   "test",
				"unknown": "value",
			},
		},
	}

	columns := []string{"folder", "state", "time", "label"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Copy input to check if it's mutated
			inputCopy := make(MessageRow, len(tt.row))
			for k, v := range tt.row {
				inputCopy[k] = v
			}

			result, err := ValidateMessageRowMetadata(columns, tt.row)

			if err != ErrMessageMetadata {
				t.Errorf("expected ErrMessageMetadata, got %v", err)
			}
			if result != nil {
				t.Errorf("expected nil result, got %v", result)
			}

			// Verify input was not mutated
			if len(tt.row) != len(inputCopy) {
				t.Errorf("input was mutated: length changed from %d to %d", len(inputCopy), len(tt.row))
			}
			for k, v := range inputCopy {
				if tt.row[k] != v {
					t.Errorf("input was mutated: key %s changed from %v to %v", k, v, tt.row[k])
				}
			}
		})
	}
}
