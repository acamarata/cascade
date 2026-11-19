package background

import "testing"

func TestStallCounterIncrementsOnNoChange(t *testing.T) {
	var s StallTracker

	if got := s.Observe(false); got != 1 {
		t.Fatalf("Observe(false) on zero tracker = %d, want 1", got)
	}
	if got := s.Prompt(); got != "" {
		t.Fatalf("Prompt() after one unchanged turn = %q, want empty string", got)
	}

	var nilTracker *StallTracker
	if got := nilTracker.Observe(false); got != 0 {
		t.Fatalf("Observe(false) on nil receiver = %d, want 0", got)
	}
	if got := nilTracker.Observe(true); got != 0 {
		t.Fatalf("Observe(true) on nil receiver = %d, want 0", got)
	}

	neg := StallTracker{Unchanged: -3}
	if got := neg.Observe(false); got != 1 {
		t.Fatalf("Observe(false) with negative count = %d, want 1", got)
	}
	if got := neg.Prompt(); got != "" {
		t.Fatalf("Prompt() after normalized increment = %q, want empty string", got)
	}
}

func TestStallCounterResetsOnChange(t *testing.T) {
	var s StallTracker

	s.Observe(false)
	s.Observe(false)
	if got := s.Prompt(); got != StallPrompt {
		t.Fatalf("Prompt() before reset = %q, want %q", got, StallPrompt)
	}

	if got := s.Observe(true); got != 0 {
		t.Fatalf("Observe(true) after stall = %d, want 0", got)
	}
	if s.Unchanged != 0 {
		t.Fatalf("Unchanged after Observe(true) = %d, want 0", s.Unchanged)
	}
	if s.Pending {
		t.Fatal("Pending after Observe(true) = true, want false")
	}
	if got := s.Prompt(); got != "" {
		t.Fatalf("Prompt() after reset = %q, want empty string", got)
	}
}

func TestCEOPromptAfterTwoUnchangedTurns(t *testing.T) {
	var s StallTracker

	if got := s.Observe(false); got != 1 {
		t.Fatalf("first Observe(false) = %d, want 1", got)
	}
	if got := s.Prompt(); got != "" {
		t.Fatalf("Prompt() after first unchanged turn = %q, want empty string", got)
	}

	if got := s.Observe(false); got != 2 {
		t.Fatalf("second Observe(false) = %d, want 2", got)
	}
	if got := s.Prompt(); got != StallPrompt {
		t.Fatalf("Prompt() after two unchanged turns = %q, want %q", got, StallPrompt)
	}

	if got := s.Observe(false); got != 2 {
		t.Fatalf("third Observe(false) = %d, want saturated 2", got)
	}
	if got := s.Prompt(); got != StallPrompt {
		t.Fatalf("Prompt() after saturation = %q, want %q", got, StallPrompt)
	}

	before := s
	first := s.Prompt()
	second := s.Prompt()
	if first != second {
		t.Fatalf("repeated Prompt() returned %q then %q, want identical", first, second)
	}
	if s != before {
		t.Fatalf("Prompt() mutated tracker: got %+v, want %+v", s, before)
	}

	var nilTracker *StallTracker
	if got := nilTracker.Prompt(); got != "" {
		t.Fatalf("Prompt() on nil receiver = %q, want empty string", got)
	}
}
