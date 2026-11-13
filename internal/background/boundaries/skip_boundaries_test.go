package boundaries

import (
	"errors"
	"testing"
)

// TestBoundarySkippedAndLogged ensures boundary steps are skipped and recorded
// verbatim while non-boundary steps are ignored.
func TestBoundarySkippedAndLogged(t *testing.T) {
	var s Supervisor
	steps := []string{"work", "BOUNDARY:review"}
	if err := s.Run(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.State.Skipped) != 1 || s.State.Skipped[0] != "BOUNDARY:review" {
		t.Fatalf("expected ['BOUNDARY:review'], got %v", s.State.Skipped)
	}
}

// TestNonBoundaryNotLogged ensures that step lists without boundaries leave
// the State.Skipped slice empty.
func TestNonBoundaryNotLogged(t *testing.T) {
	var s Supervisor
	steps := []string{"work"}
	if err := s.Run(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.State.Skipped) != 0 {
		t.Fatalf("expected empty Skipped, got %v", s.State.Skipped)
	}
}

// TestSupervisorBoundaries verifies nil receiver handling, successive call
// appends, empty string handling, and exact verbatim storage of "BOUNDARY:".
func TestSupervisorBoundaries(t *testing.T) {
	var s *Supervisor
	err := s.Run([]string{"anything"})
	if !errors.Is(err, ErrInvalidSupervisor) {
		t.Fatalf("expected ErrInvalidSupervisor, got %v", err)
	}

	var validSup Supervisor
	if err := validSup.Run([]string{"BOUNDARY:first"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(validSup.State.Skipped) != 1 || validSup.State.Skipped[0] != "BOUNDARY:first" {
		t.Fatalf("expected ['BOUNDARY:first'], got %v", validSup.State.Skipped)
	}
	if err := validSup.Run([]string{"BOUNDARY:second"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(validSup.State.Skipped) != 2 || validSup.State.Skipped[1] != "BOUNDARY:second" {
		t.Fatalf("expected appended 'BOUNDARY:second', got %v", validSup.State.Skipped)
	}
	if err := validSup.Run([]string{"", "work", "BOUNDARY:"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(validSup.State.Skipped) != 3 || validSup.State.Skipped[2] != "BOUNDARY:" {
		t.Fatalf("expected 'BOUNDARY:' verbatim, got %v", validSup.State.Skipped)
	}
}
