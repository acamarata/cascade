package docket

import (
	"strings"
	"testing"
	"time"
)

func TestMessageRowExportOmitsBody(t *testing.T) {
	validTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Helper to create a valid row
	validRow := func() MessageRow {
		return MessageRow{
			"folder": "inbox",
			"state":  "pending",
			"time":   validTime,
			"label":  "test",
		}
	}

	t.Run("valid row returns fresh map", func(t *testing.T) {
		row := validRow()
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
	})

	tests := []struct {
		name string
		row  MessageRow
	}{
		{
			name: "64-byte valid label",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  strings.Repeat("a", 64),
			},
		},
		{
			name: "65-byte label too long",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  strings.Repeat("a", 65),
			},
		},
		{
			name: "empty label",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "",
			},
		},
		{
			name: "label with slash",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "path/to/file",
			},
		},
		{
			name: "label with backslash",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "path\\to\\file",
			},
		},
		{
			name: "label is single dot",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  ".",
			},
		},
		{
			name: "label is double dot",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "..",
			},
		},
		{
			name: "label with CR",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "test\r",
			},
		},
		{
			name: "label with LF",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "test\n",
			},
		},
		{
			name: "label with NUL",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  "test\x00",
			},
		},
		{
			name: "label with invalid UTF-8",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  string([]byte{0xff, 0xfe}),
			},
		},
		{
			name: "zero time",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   time.Time{},
				"label":  "test",
			},
		},
		{
			name: "missing folder",
			row: MessageRow{
				"state": "pending",
				"time":  validTime,
				"label": "test",
			},
		},
		{
			name: "missing state",
			row: MessageRow{
				"folder": "inbox",
				"time":   validTime,
				"label":  "test",
			},
		},
		{
			name: "missing time",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"label":  "test",
			},
		},
		{
			name: "missing label",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
			},
		},
		{
			name: "wrong type for folder",
			row: MessageRow{
				"folder": 123,
				"state":  "pending",
				"time":   validTime,
				"label":  "test",
			},
		},
		{
			name: "wrong type for state",
			row: MessageRow{
				"folder": "inbox",
				"state":  123,
				"time":   validTime,
				"label":  "test",
			},
		},
		{
			name: "wrong type for time",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   "2024-01-01",
				"label":  "test",
			},
		},
		{
			name: "wrong type for label",
			row: MessageRow{
				"folder": "inbox",
				"state":  "pending",
				"time":   validTime,
				"label":  123,
			},
		},
		{
			name: "whitespace-only folder",
			row: MessageRow{
				"folder": "   ",
				"state":  "pending",
				"time":   validTime,
				"label":  "test",
			},
		},
		{
			name: "whitespace-only state",
			row: MessageRow{
				"folder": "inbox",
				"state":  "   ",
				"time":   validTime,
				"label":  "test",
			},
		},
		{
			name: "empty nonnil row",
			row:  MessageRow{},
		},
	}

	columns := []string{"folder", "state", "time", "label"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldFail := true

			// The 64-byte valid label test should pass
			if tt.name == "64-byte valid label" {
				shouldFail = false
			}

			result, err := ValidateMessageRowMetadata(columns, tt.row)

			if shouldFail {
				if err != ErrMessageMetadata {
					t.Errorf("expected ErrMessageMetadata, got %v", err)
				}
				if result != nil {
					t.Errorf("expected nil result, got %v", result)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result == nil {
					t.Error("expected non-nil result")
				}
			}
		})
	}
}
