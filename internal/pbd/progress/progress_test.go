package progress

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendChangeWritesOneJSONLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.jsonl")

	c := Change{
		Ticket:  "DKT:007",
		Status:  "ready",
		Attempt: 1,
		Label:   "triage",
	}

	err := AppendChange(path, c)
	if err != nil {
		t.Fatalf("AppendChange failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("expected mode 0600, got %o", mode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) != 2 || len(lines[1]) != 0 {
		t.Errorf("expected exactly one line ending with LF, got %q", data)
	}

	var got Change
	if err := json.Unmarshal(lines[0], &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if got != c {
		t.Errorf("expected %+v, got %+v", c, got)
	}

	err = AppendChange(path, c)
	if err != nil {
		t.Fatalf("second AppendChange failed: %v", err)
	}
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile 2 failed: %v", err)
	}
	lines2 := bytes.Split(data2, []byte{'\n'})
	if len(lines2) != 3 || len(lines2[2]) != 0 {
		t.Errorf("expected two lines ending with LF, got %q", data2)
	}

	badPath := filepath.Join(dir, "nonexistent", "progress.jsonl")
	err = AppendChange(badPath, c)
	if !errors.Is(err, ErrAppendChange) {
		t.Errorf("expected ErrAppendChange, got %v", err)
	}
}

func TestAppendChangeRejectsEmptyTicketID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.jsonl")

	if err := os.WriteFile(path, []byte("existing\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		c    Change
		path string
	}{
		{"empty ticket", Change{Ticket: "", Status: "ready", Attempt: 1, Label: "triage"}, path},
		{"whitespace ticket", Change{Ticket: "   ", Status: "ready", Attempt: 1, Label: "triage"}, path},
		{"blank path", Change{Ticket: "DKT:007", Status: "ready", Attempt: 1, Label: "triage"}, ""},
		{"attempt 0", Change{Ticket: "DKT:007", Status: "ready", Attempt: 0, Label: "triage"}, path},
		{"attempt negative", Change{Ticket: "DKT:007", Status: "ready", Attempt: -1, Label: "triage"}, path},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AppendChange(tc.path, tc.c)
			if !errors.Is(err, ErrInvalidChange) {
				t.Errorf("expected ErrInvalidChange, got %v", err)
			}
			if err != nil && err.Error() != "D2242: invalid progress change" {
				t.Errorf("unexpected error text: %v", err)
			}
		})
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "existing\n" {
		t.Errorf("existing bytes changed to %q", data)
	}

	nonexistent := filepath.Join(dir, "no.jsonl")
	err = AppendChange(nonexistent, Change{Ticket: "   ", Status: "ready", Attempt: 1, Label: "triage"})
	if !errors.Is(err, ErrInvalidChange) {
		t.Errorf("expected ErrInvalidChange, got %v", err)
	}
	if _, err := os.Stat(nonexistent); !os.IsNotExist(err) {
		t.Errorf("nonexistent path created")
	}
}
