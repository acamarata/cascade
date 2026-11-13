package progress

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAppendChangeRejectsUnknownStatus verifies that blank or unknown/case-mismatched status values are rejected.
// It checks that ErrInvalidChange is returned and no file mutation or creation occurs on invalid input.
func TestAppendChangeRejectsUnknownStatus(t *testing.T) {
	dir, err := os.MkdirTemp("", "progress_reject_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	nonExistent := filepath.Join(dir, "nonexistent.jsonl")

	tests := []struct {
		name   string
		change Change
	}{
		{
			name: "blank status",
			change: Change{
				Ticket:  "TICKET-1",
				Status:  "",
				Attempt: 1,
			},
		},
		{
			name: "unknown status",
			change: Change{
				Ticket:  "TICKET-1",
				Status:  "UNKNOWN",
				Attempt: 1,
			},
		},
		{
			name: "case-mismatched status (lowercase)",
			change: Change{
				Ticket:  "TICKET-1",
				Status:  "todo",
				Attempt: 1,
			},
		},
		{
			name: "case-mismatched status (mixed case)",
			change: Change{
				Ticket:  "TICKET-1",
				Status:  "In_Progress",
				Attempt: 1,
			},
		},
		{
			name: "blank ticket",
			change: Change{
				Ticket:  "   ",
				Status:  "TODO",
				Attempt: 1,
			},
		},
		{
			name: "empty ticket",
			change: Change{
				Ticket:  "",
				Status:  "TODO",
				Attempt: 1,
			},
		},
		{
			name: "invalid attempt zero",
			change: Change{
				Ticket:  "TICKET-1",
				Status:  "TODO",
				Attempt: 0,
			},
		},
		{
			name: "invalid attempt negative",
			change: Change{
				Ticket:  "TICKET-1",
				Status:  "TODO",
				Attempt: -1,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := AppendChange(nonExistent, tc.change)
			if !errors.Is(err, ErrInvalidChange) {
				t.Errorf("expected ErrInvalidChange (D2242), got %v", err)
			}

			if _, statErr := os.Stat(nonExistent); !os.IsNotExist(statErr) {
				t.Errorf("expected no file creation, but file exists or stat returned %v", statErr)
			}
		})
	}
}
