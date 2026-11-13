package docket

import "testing"

func TestMessageRowInsertRejectsBodyField(t *testing.T) {
	tests := []struct {
		name string
		row  MessageRow
	}{
		{"body field lowercase", mrpRow(map[string]any{"body": "message content"})},
		{"Body field capitalized", mrpRow(map[string]any{"Body": "message content"})},
		{"BODY field uppercase", mrpRow(map[string]any{"BODY": "message content"})},
		{"extra unknown field", mrpRow(map[string]any{"unknown": "value"})},
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
