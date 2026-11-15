package fmx

import (
	"testing"
	"time"
)

// mockClock implements Clock for testing
type mockClock struct {
	now time.Time
}

func (m *mockClock) Now() time.Time {
	return m.now
}

// mockUsageLog implements UsageLog for testing
type mockUsageLog struct {
	rows []string
}

func (m *mockUsageLog) Append(row string) {
	m.rows = append(m.rows, row)
}

func TestEpisodeStartsWhenBothHot(t *testing.T) {
	clock := &mockClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	log := &mockUsageLog{}
	tracker := NewTracker(clock, log, 10, 5)

	// First tick with both hot should start an episode
	budget, ended := tracker.Tick(true)
	if ended {
		t.Error("episode should not end immediately")
	}
	if budget != 5 {
		t.Errorf("expected lowered budget 5, got %d", budget)
	}
}

func TestEpisodeEndsAfter3MinQuiet(t *testing.T) {
	clock := &mockClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	log := &mockUsageLog{}
	tracker := NewTracker(clock, log, 10, 5)

	// Start episode
	tracker.Tick(true)

	// 2m59s of quiet - episode should still be open
	clock.now = clock.now.Add(2*time.Minute + 59*time.Second)
	budget, ended := tracker.Tick(false)
	if ended {
		t.Error("episode should not end at 2m59s")
	}
	if budget != 5 {
		t.Errorf("expected lowered budget 5 at 2m59s, got %d", budget)
	}

	// 3m00s of quiet - episode should end
	clock.now = clock.now.Add(1 * time.Second)
	budget, ended = tracker.Tick(false)
	if !ended {
		t.Error("episode should end at 3m00s")
	}
	if budget != 10 {
		t.Errorf("expected normal budget 10 at end, got %d", budget)
	}
}

func TestRunningLoopsNotInterrupted(t *testing.T) {
	clock := &mockClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	log := &mockUsageLog{}
	tracker := NewTracker(clock, log, 10, 5)

	// Start episode - budget drops to 5
	tracker.Tick(true)

	// Check admission with 2 running loops
	// With budget 5, we should be able to admit 2 loops
	if !tracker.Admit(2) {
		t.Error("should admit with 2 running when budget is 5")
	}

	// With 5 running, should block new admission
	if tracker.Admit(5) {
		t.Error("should block new admission when 5 running with budget 5")
	}

	// With 6 running (over budget), should block
	if tracker.Admit(6) {
		t.Error("should block when 6 running with budget 5")
	}
}

func TestBudgetRestoredOnEndTick(t *testing.T) {
	clock := &mockClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	log := &mockUsageLog{}
	tracker := NewTracker(clock, log, 10, 5)

	// Start episode
	tracker.Tick(true)

	// Wait 3 minutes of quiet
	clock.now = clock.now.Add(3 * time.Minute)

	// On the tick the episode ends, budget should be restored to normal
	budget, ended := tracker.Tick(false)
	if !ended {
		t.Error("episode should end at 3m")
	}
	if budget != 10 {
		t.Errorf("expected normal budget 10 on end tick, got %d", budget)
	}
}

func TestTwoUsageRowsLogged(t *testing.T) {
	clock := &mockClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	log := &mockUsageLog{}
	tracker := NewTracker(clock, log, 10, 5)

	// Start episode
	tracker.Tick(true)

	// Wait 3 minutes of quiet
	clock.now = clock.now.Add(3 * time.Minute)

	// End episode
	tracker.Tick(false)

	// Should have logged exactly 2 rows
	if len(log.rows) != 2 {
		t.Errorf("expected 2 usage rows, got %d", len(log.rows))
	}
}
