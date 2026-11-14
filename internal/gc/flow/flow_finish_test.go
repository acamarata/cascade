package flow

import (
	"testing"
	"time"
)

// mockFinishClock is a simple Clock implementation for testing Finish events.
type mockFinishClock struct {
	now    time.Time
	called bool
}

// Now returns the current time.
func (m *mockFinishClock) Now() time.Time {
	m.called = true
	return m.now
}

func TestFinishOkMovesToDone(t *testing.T) {
	clk := &mockFinishClock{now: time.Now()}
	m, err := New(clk)
	if err != nil {
		t.Fatalf("unexpected error creating machine: %v", err)
	}

	// Start the machine
	_, _, err = m.Step(Event{Kind: Start, At: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error starting machine: %v", err)
	}

	clk.called = false // Reset to ensure Finish does not read clock
	state, actions, err := m.Step(Event{Kind: Finish, OK: true})
	if err != nil {
		t.Fatalf("unexpected error finishing machine: %v", err)
	}

	if state != Done {
		t.Errorf("expected state Done, got %v", state)
	}

	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}

	if actions[0].Kind != "finish" {
		t.Errorf("expected action kind finish, got %v", actions[0].Kind)
	}

	if actions[0].Elapsed != 0 {
		t.Errorf("expected elapsed 0, got %v", actions[0].Elapsed)
	}

	if clk.called {
		t.Errorf("finish path must not read Clock")
	}
}

func TestFinishFailMovesToFailed(t *testing.T) {
	clk := &mockFinishClock{now: time.Now()}
	m, err := New(clk)
	if err != nil {
		t.Fatalf("unexpected error creating machine: %v", err)
	}

	// Start the machine
	_, _, err = m.Step(Event{Kind: Start, At: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error starting machine: %v", err)
	}

	clk.called = false // Reset to ensure Finish does not read clock
	state, actions, err := m.Step(Event{Kind: Finish, OK: false})
	if err != nil {
		t.Fatalf("unexpected error finishing machine: %v", err)
	}

	if state != Failed {
		t.Errorf("expected state Failed, got %v", state)
	}

	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}

	if actions[0].Kind != "finish" {
		t.Errorf("expected action kind finish, got %v", actions[0].Kind)
	}

	if actions[0].Elapsed != 0 {
		t.Errorf("expected elapsed 0, got %v", actions[0].Elapsed)
	}

	if clk.called {
		t.Errorf("finish path must not read Clock")
	}
}
