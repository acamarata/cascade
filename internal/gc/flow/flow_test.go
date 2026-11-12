package flow

import (
	"errors"
	"testing"
	"time"
)

type fakeClock struct {
	t time.Time
}

func (f *fakeClock) Now() time.Time {
	return f.t
}

type panicClock struct{}

func (p *panicClock) Now() time.Time {
	panic("clock read")
}

// TestStartMovesIdleToRunning verifies New reads no clock; Start{At: fixed nonzero instant}
// returns Running and start action with elapsed 0; zero anchor returns ErrInvalidStart and Idle.
func TestStartMovesIdleToRunning(t *testing.T) {
	// New reads no clock; we pass a clock that panics if Now() is called.
	m, err := New(&panicClock{})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	fixedTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state, actions, err := m.Step(Event{Kind: Start, At: fixedTime})
	if err != nil {
		t.Fatalf("Step failed: %v", err)
	}
	if state != Running {
		t.Errorf("expected Running, got %s", state)
	}
	if len(actions) != 1 || actions[0].Kind != "start" || actions[0].Elapsed != 0 {
		t.Errorf("unexpected actions: %v", actions)
	}

	// zero anchor returns ErrInvalidStart and Idle
	m2, _ := New(&fakeClock{t: time.Now()})
	state2, actions2, err2 := m2.Step(Event{Kind: Start, At: time.Time{}})
	if !errors.Is(err2, ErrInvalidStart) {
		t.Errorf("expected ErrInvalidStart, got %v", err2)
	}
	if state2 != Idle {
		t.Errorf("expected Idle, got %s", state2)
	}
	if actions2 != nil {
		t.Errorf("expected nil actions, got %v", actions2)
	}
}

// TestIllegalEventKeepsState verifies Finish on Idle, unknown events, repeated Start and
// terminal events return ErrIllegalEvent, unchanged state, nil actions; nil/typed-nil
// Clock returns ErrInvalidClock.
func TestIllegalEventKeepsState(t *testing.T) {
	// Finish on Idle
	m, _ := New(&fakeClock{t: time.Now()})
	state, actions, err := m.Step(Event{Kind: Finish})
	if !errors.Is(err, ErrIllegalEvent) {
		t.Errorf("expected ErrIllegalEvent, got %v", err)
	}
	if state != Idle {
		t.Errorf("expected Idle, got %s", state)
	}
	if actions != nil {
		t.Errorf("expected nil actions, got %v", actions)
	}

	// unknown events
	m2, _ := New(&fakeClock{t: time.Now()})
	state, _, err = m2.Step(Event{Kind: "unknown"})
	if !errors.Is(err, ErrIllegalEvent) {
		t.Errorf("expected ErrIllegalEvent, got %v", err)
	}
	if state != Idle {
		t.Errorf("expected Idle, got %s", state)
	}

	// repeated Start
	m3, _ := New(&fakeClock{t: time.Now()})
	_, _, _ = m3.Step(Event{Kind: Start, At: time.Now()})
	state, _, err = m3.Step(Event{Kind: Start, At: time.Now()})
	if !errors.Is(err, ErrIllegalEvent) {
		t.Errorf("expected ErrIllegalEvent, got %v", err)
	}
	if state != Running {
		t.Errorf("expected Running, got %s", state)
	}

	// nil/typed-nil Clock returns ErrInvalidClock
	_, err = New(nil)
	if !errors.Is(err, ErrInvalidClock) {
		t.Errorf("expected ErrInvalidClock for nil, got %v", err)
	}

	var typedNil *fakeClock
	_, err = New(typedNil)
	if !errors.Is(err, ErrInvalidClock) {
		t.Errorf("expected ErrInvalidClock for typed-nil, got %v", err)
	}
}
