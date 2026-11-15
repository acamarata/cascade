package doneopen

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDoneOpenReviewNotesOpen verifies that ReviewNotesOpen resolves to
// StatusDoneOpen without error and that State round-trips correctly via JSON.
func TestDoneOpenReviewNotesOpen(t *testing.T) {
	status, err := ResolveDoneOpen(ReviewNotesOpen)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusDoneOpen {
		t.Fatalf("expected status %q, got %q", StatusDoneOpen, status)
	}

	state := State{Status: StatusDoneOpen, Why: ReviewNotesOpen}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	s := string(b)
	if !strings.Contains(s, `"status":"doneopen"`) {
		t.Fatalf("missing status key in JSON: %s", s)
	}
	if !strings.Contains(s, `"why":`) {
		t.Fatalf("missing why key in JSON: %s", s)
	}

	var got State
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if got.Status != state.Status {
		t.Errorf("Status mismatch: got %q, want %q", got.Status, state.Status)
	}
	if got.Why != state.Why {
		t.Errorf("Why mismatch: got %q, want %q", got.Why, state.Why)
	}
}

// TestDoneOpenNoReview verifies that NoReview resolves to StatusDoneOpen
// without error and marshals exactly to {"status":"doneopen","why":"no_review"}.
func TestDoneOpenNoReview(t *testing.T) {
	status, err := ResolveDoneOpen(NoReview)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusDoneOpen {
		t.Fatalf("expected status %q, got %q", StatusDoneOpen, status)
	}

	state := State{Status: StatusDoneOpen, Why: NoReview}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	expected := `{"status":"doneopen","why":"no_review"}`
	if string(b) != expected {
		t.Fatalf("expected JSON %s, got %s", expected, string(b))
	}
}
