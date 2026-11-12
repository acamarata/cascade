package attempt

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const overflowValidTS = "2024-05-01T12:00:00.123456789Z"

func TestFormatOverflowJSONValidRoundTrip(t *testing.T) {
	row := OverflowRow{
		TS:      overflowValidTS,
		Ticket:  "TICKET-1",
		Attempt: 3,
		Reason:  "reason with\nnewline",
		Dropped: 0,
	}
	data, err := FormatOverflowJSON(row)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("expected trailing newline")
	}
	if bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatalf("expected exactly one newline")
	}

	// Check JSON escaped newline
	if !bytes.Contains(data, []byte(`\n`)) {
		t.Errorf("expected newline in Reason to be escaped in JSON")
	}

	var m map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSuffix(data, []byte{'\n'}), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(m) != 5 {
		t.Errorf("expected 5 keys, got %d", len(m))
	}
	if m["ts"] != overflowValidTS {
		t.Errorf("ts: %v", m["ts"])
	}
	if m["ticket"] != "TICKET-1" {
		t.Errorf("ticket: %v", m["ticket"])
	}
	if m["attempt"] != float64(3) {
		t.Errorf("attempt: %v", m["attempt"])
	}
	if m["reason"] != "reason with\nnewline" {
		t.Errorf("reason: %v", m["reason"])
	}
	if m["dropped"] != float64(0) {
		t.Errorf("dropped: %v", m["dropped"])
	}
}

func TestFormatOverflowJSONInvalidRows(t *testing.T) {
	// Create a temporary directory to test "no file effect" for AppendOverflowRow
	dir := t.TempDir()
	path := filepath.Join(dir, "escalations.jsonl")

	cases := []OverflowRow{
		{TS: overflowValidTS, Ticket: "", Attempt: 1, Reason: "r", Dropped: 0},
		{TS: overflowValidTS, Ticket: " ", Attempt: 1, Reason: "r", Dropped: 0},
		{TS: overflowValidTS, Ticket: "T", Attempt: 1, Reason: "", Dropped: 0},
		{TS: overflowValidTS, Ticket: "T", Attempt: 1, Reason: " ", Dropped: 0},
		{TS: overflowValidTS, Ticket: "T", Attempt: 0, Reason: "r", Dropped: 0},
		{TS: overflowValidTS, Ticket: "T", Attempt: 1, Reason: "r", Dropped: -1},
		{TS: "bad", Ticket: "T", Attempt: 1, Reason: "r", Dropped: 0},
	}

	for i, row := range cases {
		// Test FormatOverflowJSON
		_, err := FormatOverflowJSON(row)
		if !errors.Is(err, ErrInvalidOverflow) {
			t.Errorf("FormatOverflowJSON case %d: expected ErrInvalidOverflow, got %v", i, err)
		}

		// Test AppendOverflowRow has no file effect
		err = AppendOverflowRow(dir, row)
		if !errors.Is(err, ErrInvalidOverflow) {
			t.Errorf("AppendOverflowRow case %d: expected ErrInvalidOverflow, got %v", i, err)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			t.Errorf("AppendOverflowRow case %d: file was created despite invalid row", i)
		}
	}
}

func TestFormatOverflowJSONBlankDir(t *testing.T) {
	row := OverflowRow{
		TS: overflowValidTS, Ticket: "T", Attempt: 1, Reason: "r", Dropped: 0,
	}
	err := AppendOverflowRow("", row)
	if !errors.Is(err, ErrInvalidOverflow) {
		t.Errorf("expected ErrInvalidOverflow for blank dir, got %v", err)
	}
}
