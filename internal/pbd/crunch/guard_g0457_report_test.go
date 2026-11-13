package crunch

import (
	"errors"
	"testing"
	"time"
)

// TestStatus429TriggersTenMinuteCooldown verifies that ReportStatus with 429
// sets a 10-minute cooldown, repeated 429s extend it, other statuses are no-ops,
// another lane is unaffected, and invalid inputs return ErrInvalidGuard.
func TestStatus429TriggersTenMinuteCooldown(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := base
	e := &ExecEnv{
		Now:   func() time.Time { return now },
		Quota: func(lane string) error { return nil },
	}

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

	// Invalid inputs return ErrInvalidGuard without mutation.
	existingDeadline := e.cooldownUntil[lane]

	// Nil receiver.
	var nilEnv *ExecEnv
	if err := nilEnv.ReportStatus(lane, 429); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("nil receiver: expected ErrInvalidGuard, got %v", err)
	}

	// Empty lane.
	if err := e.ReportStatus("", 429); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("empty lane: expected ErrInvalidGuard, got %v", err)
	}

	// Status below 100.
	if err := e.ReportStatus(lane, 99); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("status 99: expected ErrInvalidGuard, got %v", err)
	}

	// Status above 599.
	if err := e.ReportStatus(lane, 600); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("status 600: expected ErrInvalidGuard, got %v", err)
	}

	// Nil Now.
	eNoNow := &ExecEnv{Quota: func(string) error { return nil }}
	if err := eNoNow.ReportStatus(lane, 429); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("nil Now: expected ErrInvalidGuard, got %v", err)
	}

	// Nil Quota.
	eNoQuota := &ExecEnv{Now: func() time.Time { return now }}
	if err := eNoQuota.ReportStatus(lane, 429); !errors.Is(err, ErrInvalidGuard) {
		t.Fatalf("nil Quota: expected ErrInvalidGuard, got %v", err)
	}

	// Verify state was not mutated by invalid calls.
	if d := e.cooldownUntil[lane]; !d.Equal(existingDeadline) {
		t.Fatalf("state mutated after invalid call: got %v, want %v", d, existingDeadline)
	}
}
