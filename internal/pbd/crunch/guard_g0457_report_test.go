package crunch

import (
	"errors"
	"testing"
	"time"
)

// newReportEnv returns an ExecEnv whose clock reads *now.
func newReportEnv(now *time.Time) *ExecEnv {
	return &ExecEnv{
		Now:   func() time.Time { return *now },
		Quota: func(string) error { return nil },
	}
}

// TestStatus429TriggersTenMinuteCooldown verifies that ReportStatus with 429
// sets a 10-minute cooldown, repeated 429s extend it, other statuses are no-ops,
// and another lane is unaffected.
func TestStatus429TriggersTenMinuteCooldown(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := base
	e := newReportEnv(&now)

	lane := "api"

	// First 429 sets deadline to base + 10m.
	if err := e.ReportStatus(lane, 429); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	deadline := e.cooldownUntil[lane]
	expected := base.Add(10 * time.Minute)
	if !deadline.Equal(expected) {
		t.Fatalf("expected deadline %v, got %v", expected, deadline)
	}

	// Guard should reject during cooldown.
	if err := e.Guard(lane); !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown, got %v", err)
	}

	// Advance time by 5 minutes; repeated 429 extends from latest report.
	now = base.Add(5 * time.Minute)
	if err := e.ReportStatus(lane, 429); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	deadline = e.cooldownUntil[lane]
	expected = now.Add(10 * time.Minute)
	if !deadline.Equal(expected) {
		t.Fatalf("expected extended deadline %v, got %v", expected, deadline)
	}

	// Other valid status does not clear or shorten cooldown.
	now = base.Add(8 * time.Minute)
	if err := e.ReportStatus(lane, 200); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d := e.cooldownUntil[lane]; !d.Equal(expected) {
		t.Fatalf("status 200 changed deadline: got %v, want %v", d, expected)
	}

	// Another lane is unaffected.
	otherLane := "web"
	if err := e.ReportStatus(otherLane, 200); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := e.cooldownUntil[otherLane]; ok {
		t.Fatal("non-429 status should not create cooldown entry for other lane")
	}
}

// TestReportStatusInvalidInputsReturnErrInvalidGuard verifies that invalid
// inputs return ErrInvalidGuard without mutating state.
func TestReportStatusInvalidInputsReturnErrInvalidGuard(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := base
	e := newReportEnv(&now)
	lane := "api"

	if err := e.ReportStatus(lane, 429); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	existingDeadline := e.cooldownUntil[lane]

	var nilEnv *ExecEnv
	cases := []struct {
		name   string
		env    *ExecEnv
		lane   string
		status int
	}{
		{"nil receiver", nilEnv, lane, 429},
		{"empty lane", e, "", 429},
		{"status 99", e, lane, 99},
		{"status 600", e, lane, 600},
		{"nil Now", &ExecEnv{Quota: func(string) error { return nil }}, lane, 429},
		{"nil Quota", &ExecEnv{Now: func() time.Time { return now }}, lane, 429},
	}
	for _, c := range cases {
		if err := c.env.ReportStatus(c.lane, c.status); !errors.Is(err, ErrInvalidGuard) {
			t.Fatalf("%s: expected ErrInvalidGuard, got %v", c.name, err)
		}
	}

	// Verify state was not mutated by invalid calls.
	if d := e.cooldownUntil[lane]; !d.Equal(existingDeadline) {
		t.Fatalf("state mutated after invalid call: got %v, want %v", d, existingDeadline)
	}
}
