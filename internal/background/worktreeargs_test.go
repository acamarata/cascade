package background

import (
	"strings"
	"testing"
)

// TestWorktreeArgsUnmergedLaneAddsBase verifies that when a ticket edits an
// unmerged lane, the --base flag is included with the correct lane reference.
func TestWorktreeArgsUnmergedLaneAddsBase(t *testing.T) {
	ticket := Ticket{
		ID:        "t-123",
		Lane:      "acct-a",
		Prompt:    "fix billing",
		EditsLane: true,
		Files:     []string{"billing.go"},
	}
	unmerged := map[string]bool{"acct-a": true}

	args, err := WorktreeArgs(ticket, unmerged)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundBase := false
	for i, arg := range args {
		if arg == "--base" && i+1 < len(args) && args[i+1] == "lane/acct-a" {
			foundBase = true
			break
		}
	}
	if !foundBase {
		t.Errorf("expected args to contain '--base' followed by 'lane/acct-a', got %v", args)
	}
}

// TestWorktreeArgsMergedLaneNoBase verifies that when a ticket references a
// merged lane (not in unmerged set), no --base flag is added.
func TestWorktreeArgsMergedLaneNoBase(t *testing.T) {
	ticket := Ticket{
		ID:        "t-456",
		Lane:      "acct-a",
		Prompt:    "update docs",
		EditsLane: false,
		Files:     []string{"README.md"},
	}
	unmerged := map[string]bool{}

	args, err := WorktreeArgs(ticket, unmerged)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, arg := range args {
		if arg == "--base" {
			t.Errorf("expected no --base flag for merged lane, got %v", args)
			break
		}
	}
}

// TestWorktreeArgsNoLaneNoBase verifies that when a ticket has no lane,
// no --base flag is added.
func TestWorktreeArgsNoLaneNoBase(t *testing.T) {
	ticket := Ticket{
		ID:        "t-789",
		Lane:      "",
		Prompt:    "general fix",
		EditsLane: false,
		Files:     []string{"main.go"},
	}
	unmerged := map[string]bool{}

	args, err := WorktreeArgs(ticket, unmerged)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, arg := range args {
		if arg == "--base" {
			t.Errorf("expected no --base flag when no lane, got %v", args)
			break
		}
	}
}

// TestWorktreeArgsEmptyLaneNameRejected verifies that a ticket marked as
// editing a lane but with an empty lane name returns an error.
func TestWorktreeArgsEmptyLaneNameRejected(t *testing.T) {
	ticket := Ticket{
		ID:        "t-000",
		Lane:      "",
		Prompt:    "something",
		EditsLane: true,
		Files:     []string{"x.go"},
	}
	unmerged := map[string]bool{}

	_, err := WorktreeArgs(ticket, unmerged)
	if err == nil {
		t.Fatal("expected error for empty lane name with EditsLane=true, got nil")
	}
	if !strings.Contains(err.Error(), "lane") || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected error mentioning empty lane, got: %v", err)
	}
}
