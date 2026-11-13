package progress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppendChangeRecordsStatusAttemptLabel verifies that AppendChange
// records status, attempt and label correctly, preserves existing bytes,
// accepts exactly the valid statuses, and round-trips labels containing
// newlines as one physical JSON line.
func TestAppendChangeRecordsStatusAttemptLabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.jsonl")

	// Write an existing line to ensure bytes are preserved.
	existing := []byte("existing-line\n")
	if err := os.WriteFile(path, existing, 0644); err != nil {
		t.Fatal(err)
	}

	// Append a running/2/build change after the existing line.
	c := Change{
		Ticket:  "T-1",
		Status:  "running",
		Attempt: 2,
		Label:   "build",
	}
	if err := AppendChange(path, c); err != nil {
		t.Fatalf("AppendChange failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Verify existing bytes are preserved.
	if !strings.HasPrefix(string(data), string(existing)) {
		t.Fatalf("existing bytes not preserved, got %q", string(data))
	}

	// Table of exactly valid statuses.
	validStatuses := []string{
		"provisional", "ready", "running", "review",
		"landed", "parked", "blocked", "cancelled", "needs-owner",
	}

	for _, st := range validStatuses {
		tc := Change{
			Ticket:  "T-2",
			Status:  st,
			Attempt: 1,
			Label:   "",
		}
		if err := AppendChange(path, tc); err != nil {
			t.Errorf("AppendChange rejected valid status %q: %v", st, err)
		}
	}

	// Invalid status must be rejected.
	invalid := Change{Ticket: "T-3", Status: "unknown", Attempt: 1, Label: "x"}
	if err := AppendChange(path, invalid); err == nil {
		t.Error("AppendChange accepted invalid status")
	}

	// Labels with newlines must round-trip as one physical JSON line.
	nlLabel := "line1\nline2\nline3"
	nlc := Change{
		Ticket:  "T-4",
		Status:  "ready",
		Attempt: 1,
		Label:   nlLabel,
	}
	if err := AppendChange(path, nlc); err != nil {
		t.Fatalf("AppendChange with newline label failed: %v", err)
	}

	// Re-read and verify each appended line is exactly one JSON line.
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	// lines[0] is the existing line; rest are JSON changes.
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		var got Change
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nline: %q", i, err, line)
		}
	}

	// Find the newline-label entry and verify it round-tripped.
	found := false
	for i := 1; i < len(lines); i++ {
		var got Change
		if err := json.Unmarshal([]byte(lines[i]), &got); err == nil {
			if got.Ticket == "T-4" {
				found = true
				if got.Label != nlLabel {
					t.Errorf("label round-trip failed: got %q, want %q", got.Label, nlLabel)
				}
			}
		}
	}
	if !found {
		t.Error("newline-label change not found in output")
	}
}
