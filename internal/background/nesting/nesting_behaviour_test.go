package nesting

import (
	"testing"
)

func TestNewLimiter(t *testing.T) {
	tests := []struct {
		name string
		lane LaneWidth
	}{
		{
			name: "valid lane",
			lane: &fakeLane{capacity: 1},
		},
		{
			name: "nil lane",
			lane: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLimiter(tt.lane)
			if l == nil {
				t.Fatal("NewLimiter returned nil")
			}
			if l.Running() != 0 {
				t.Errorf("expected Running() == 0, got %d", l.Running())
			}
			if l.Queued() != nil {
				t.Errorf("expected Queued() == nil, got %v", l.Queued())
			}
		})
	}
}

func TestAdmitNegativeDepth(t *testing.T) {
	lane := &fakeLane{capacity: 1}
	l := NewLimiter(lane)

	task := Task{ID: "neg", Depth: -5}
	state, err := l.Admit(task)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state != "running" {
		t.Errorf("expected state running, got %q", state)
	}
	if l.Running() != 1 {
		t.Errorf("expected Running() == 1, got %d", l.Running())
	}
}

func TestAdmitDepthGreaterThan10(t *testing.T) {
	lane := &fakeLane{capacity: 1}
	l := NewLimiter(lane)

	task := Task{ID: "deep", Depth: 11}
	state, err := l.Admit(task)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state != "queued" {
		t.Errorf("expected state queued, got %q", state)
	}
	if l.Running() != 0 {
		t.Errorf("expected Running() == 0, got %d", l.Running())
	}
	q := l.Queued()
	if len(q) != 1 || q[0].ID != "deep" {
		t.Errorf("expected queued to contain deep, got %v", q)
	}
}

func TestAdmitRunningLimitReached(t *testing.T) {
	lane := &fakeLane{capacity: 1000}
	l := NewLimiter(lane)

	// Fill up to 400 running tasks
	for i := 0; i < 400; i++ {
		task := Task{ID: "run", Depth: 1}
		state, err := l.Admit(task)
		if err != nil {
			t.Fatalf("Admit returned error: %v", err)
		}
		if state != "running" {
			t.Fatalf("expected state running for task %d, got %q", i, state)
		}
	}

	if l.Running() != 400 {
		t.Fatalf("expected Running() == 400, got %d", l.Running())
	}

	// Next task should be queued even though lane has capacity
	task := Task{ID: "over", Depth: 1}
	state, err := l.Admit(task)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state != "queued" {
		t.Errorf("expected state queued, got %q", state)
	}
	if l.Running() != 400 {
		t.Errorf("expected Running() == 400, got %d", l.Running())
	}
	q := l.Queued()
	if len(q) != 1 || q[0].ID != "over" {
		t.Errorf("expected queued to contain over, got %v", q)
	}
}

func TestAdmitWithQueueNotEmpty(t *testing.T) {
	lane := &fakeLane{capacity: 1}
	l := NewLimiter(lane)

	// First task exhausts capacity
	task1 := Task{ID: "t1", Depth: 1}
	state1, err := l.Admit(task1)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state1 != "running" {
		t.Fatalf("expected state running, got %q", state1)
	}

	// Second task goes to queue
	task2 := Task{ID: "t2", Depth: 1}
	state2, err := l.Admit(task2)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state2 != "queued" {
		t.Fatalf("expected state queued, got %q", state2)
	}

	// Third task should also queue (queue not empty) even if we could acquire
	// Note: lane capacity is exhausted anyway, but this tests the queue check
	task3 := Task{ID: "t3", Depth: 1}
	state3, err := l.Admit(task3)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state3 != "queued" {
		t.Errorf("expected state queued, got %q", state3)
	}
	q := l.Queued()
	if len(q) != 2 {
		t.Errorf("expected 2 tasks in queue, got %d", len(q))
	}
}

func TestAdmitWithNilLane(t *testing.T) {
	l := NewLimiter(nil)

	task := Task{ID: "nil", Depth: 1}
	state, err := l.Admit(task)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state != "queued" {
		t.Errorf("expected state queued, got %q", state)
	}
	if l.Running() != 0 {
		t.Errorf("expected Running() == 0, got %d", l.Running())
	}
	q := l.Queued()
	if len(q) != 1 || q[0].ID != "nil" {
		t.Errorf("expected queued to contain nil, got %v", q)
	}
}

func TestAdmitTryAcquireFails(t *testing.T) {
	lane := &fakeLane{capacity: 0} // No capacity
	l := NewLimiter(lane)

	task := Task{ID: "fail", Depth: 1}
	state, err := l.Admit(task)
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if state != "queued" {
		t.Errorf("expected state queued, got %q", state)
	}
	if l.Running() != 0 {
		t.Errorf("expected Running() == 0, got %d", l.Running())
	}
	q := l.Queued()
	if len(q) != 1 || q[0].ID != "fail" {
		t.Errorf("expected queued to contain fail, got %v", q)
	}
}

func TestQueuedReturnsCopy(t *testing.T) {
	lane := &fakeLane{capacity: 1}
	l := NewLimiter(lane)

	// Queue some tasks
	lane.capacity = 0 // Force queuing
	task1 := Task{ID: "t1", Depth: 1}
	task2 := Task{ID: "t2", Depth: 1}
	l.Admit(task1)
	l.Admit(task2)

	q1 := l.Queued()
	if len(q1) != 2 {
		t.Fatalf("expected 2 tasks in queue, got %d", len(q1))
	}

	// Modify the returned slice
	q1[0].ID = "modified"

	// Get queue again and verify original is unchanged
	q2 := l.Queued()
	if q2[0].ID != "t1" {
		t.Errorf("modifying returned slice affected internal queue: got %q, expected t1", q2[0].ID)
	}
}

func TestRunning(t *testing.T) {
	lane := &fakeLane{capacity: 3}
	l := NewLimiter(lane)

	if l.Running() != 0 {
		t.Errorf("expected Running() == 0, got %d", l.Running())
	}

	for i := 0; i < 3; i++ {
		task := Task{ID: "run", Depth: 1}
		state, err := l.Admit(task)
		if err != nil {
			t.Fatalf("Admit returned error: %v", err)
		}
		if state != "running" {
			t.Fatalf("expected state running, got %q", state)
		}
	}

	if l.Running() != 3 {
		t.Errorf("expected Running() == 3, got %d", l.Running())
	}
}

func TestIsNilLane(t *testing.T) {
	tests := []struct {
		name     string
		lane     LaneWidth
		expected bool
	}{
		{
			name:     "untyped nil",
			lane:     nil,
			expected: true,
		},
		{
			name:     "valid pointer",
			lane:     &fakeLane{capacity: 1},
			expected: false,
		},
		{
			name:     "typed nil pointer",
			lane:     (*fakeLane)(nil),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNilLane(tt.lane)
			if result != tt.expected {
				t.Errorf("isNilLane() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
