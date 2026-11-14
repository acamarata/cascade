package nesting

import (
	"testing"
)

// TestDepth11Queued verifies that a task with depth > 10 is queued,
// and that Release promotes it as a new root continuation with Depth=0.
func TestDepth11Queued(t *testing.T) {
	lane := &fakeLane{capacity: 1}
	limiter := NewLimiter(lane)

	// Admit a task with depth 11
	task := Task{ID: "deep-task", Depth: 11}
	status, err := limiter.Admit(task)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if status != "queued" {
		t.Errorf("expected status 'queued', got %q", status)
	}
	if limiter.Running() != 0 {
		t.Errorf("expected Running() == 0, got %d", limiter.Running())
	}

	// Release should promote the queued task as Depth=0
	promoted, ok := limiter.Release()
	if !ok {
		t.Fatal("expected Release to promote queued task")
	}
	if promoted.ID != "deep-task" {
		t.Errorf("expected promoted task ID 'deep-task', got %q", promoted.ID)
	}
	if promoted.Depth != 0 {
		t.Errorf("expected promoted task Depth=0, got %d", promoted.Depth)
	}
	if limiter.Running() != 1 {
		t.Errorf("expected Running() == 1 after promotion, got %d", limiter.Running())
	}

	// Test nil lane queues safely
	nilLimiter := NewLimiter(nil)
	status2, err2 := nilLimiter.Admit(Task{ID: "nil-lane-task", Depth: 11})
	if err2 != nil {
		t.Fatalf("Admit with nil lane returned error: %v", err2)
	}
	if status2 != "queued" {
		t.Errorf("expected status 'queued' with nil lane, got %q", status2)
	}
}
