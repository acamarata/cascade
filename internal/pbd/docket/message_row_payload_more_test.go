package docket

import (
	"strings"
	"testing"
	"time"
)

// mrpRow builds a valid message row, applies overrides, then drops keys.
func mrpRow(over map[string]any, drop ...string) MessageRow {
	row := MessageRow{
		"folder": "inbox",
		"state":  "pending",
		"time":   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		"label":  "test",
	}
	for k, v := range over {
		row[k] = v
	}
	for _, k := range drop {
		delete(row, k)
	}
	return row
}

func TestMessageRowExportOmitsBody(t *testing.T) {
	validTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	row := mrpRow(nil)
	columns := []string{"folder", "state", "time", "label"}

	result, err := ValidateMessageRowMetadata(columns, row)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 4 {
		t.Errorf("expected 4 keys, got %d", len(result))
	}

	// Verify values match input
	if result["folder"] != "inbox" {
		t.Errorf("folder mismatch: got %v", result["folder"])
	}
	if result["state"] != "pending" {
		t.Errorf("state mismatch: got %v", result["state"])
	}
	if result["time"] != validTime {
		t.Errorf("time mismatch: got %v", result["time"])
	}
	if result["label"] != "test" {
		t.Errorf("label mismatch: got %v", result["label"])
	}

	// Verify result is a fresh map (mutating it doesn't affect input)
	result["folder"] = "modified"
	if row["folder"] == "modified" {
		t.Error("mutating result affected input")
	}
}

func TestMessageRowAcceptsMaxLabel(t *testing.T) {
	columns := []string{"folder", "state", "time", "label"}
	row := mrpRow(map[string]any{"label": strings.Repeat("a", 64)})

	result, err := ValidateMessageRowMetadata(columns, row)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
}

func TestMessageRowRejectsInvalidRows(t *testing.T) {
	tests := []struct {
		name string
		row  MessageRow
	}{
		{"65-byte label too long", mrpRow(map[string]any{"label": strings.Repeat("a", 65)})},
		{"empty label", mrpRow(map[string]any{"label": ""})},
		{"label with slash", mrpRow(map[string]any{"label": "path/to/file"})},
		{"label with backslash", mrpRow(map[string]any{"label": "path\\to\\file"})},
		{"label is single dot", mrpRow(map[string]any{"label": "."})},
		{"label is double dot", mrpRow(map[string]any{"label": ".."})},
		{"label with CR", mrpRow(map[string]any{"label": "test\r"})},
		{"label with LF", mrpRow(map[string]any{"label": "test\n"})},
		{"label with NUL", mrpRow(map[string]any{"label": "test\x00"})},
		{"label with invalid UTF-8", mrpRow(map[string]any{"label": string([]byte{0xff, 0xfe})})},
		{"zero time", mrpRow(map[string]any{"time": time.Time{}})},
		{"missing folder", mrpRow(nil, "folder")},
		{"missing state", mrpRow(nil, "state")},
		{"missing time", mrpRow(nil, "time")},
		{"missing label", mrpRow(nil, "label")},
		{"wrong type for folder", mrpRow(map[string]any{"folder": 123})},
		{"wrong type for state", mrpRow(map[string]any{"state": 123})},
		{"wrong type for time", mrpRow(map[string]any{"time": "2024-01-01"})},
		{"wrong type for label", mrpRow(map[string]any{"label": 123})},
		{"whitespace-only folder", mrpRow(map[string]any{"folder": "   "})},
		{"whitespace-only state", mrpRow(map[string]any{"state": "   "})},
		{"empty nonnil row", MessageRow{}},
	}

	columns := []string{"folder", "state", "time", "label"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ValidateMessageRowMetadata(columns, tt.row)
			if err != ErrMessageMetadata {
				t.Errorf("expected ErrMessageMetadata, got %v", err)
			}
			if result != nil {
				t.Errorf("expected nil result, got %v", result)
			}
		})
	}
}
