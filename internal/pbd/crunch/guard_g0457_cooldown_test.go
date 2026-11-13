package crunch

import (
	"errors"
	"testing"
	"time"
)

func TestAdmissionRejectedDuringCooldown(t *testing.T) {
	var (
		now     time.Time
		called  int
		quotaOk bool = true
	)

	env := &ExecEnv{
		Now: func() time.Time { return now },
		Quota: func(lane string) error {
			called++
			if !quotaOk {
				return errors.New("denied")
			}
			return nil
		},
	}

	lane := "api"
	if err := env.ReportStatus(lane, 429); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// At start
	called = 0
	err := env.Guard(lane)
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown at start, got %v", err)
	}
	if called != 1 {
		t.Fatalf("expected Quota to be called once at start, called %d times", called)
	}

	// At 10*time.Minute - time.Nanosecond
	now = now.Add(10*time.Minute - time.Nanosecond)
	called = 0
	err = env.Guard(lane)
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown at deadline-1ns, got %v", err)
	}
	if called != 1 {
		t.Fatalf("expected Quota to be called once at deadline-1ns, called %d times", called)
	}

	// Quota failure takes precedence and preserves state
	now = now.Add(time.Nanosecond) // exactly 10*time.Minute
	quotaOk = false
	called = 0
	err = env.Guard(lane)
	if !errors.Is(err, ErrQuotaFloor) {
		t.Fatalf("expected ErrQuotaFloor, got %v", err)
	}
	if called != 1 {
		t.Fatalf("expected Quota to be called once on failure, called %d times", called)
	}
	// State preserved: cooldown map not mutated by quota failure
	if env.cooldownUntil == nil {
		t.Fatalf("cooldown map is nil")
	}
	if _, ok := env.cooldownUntil[lane]; !ok {
		t.Fatalf("cooldown map entry removed on Quota failure")
	}
}

func TestAdmissionAllowedAfterCooldownExpires(t *testing.T) {
	var now time.Time

	env := &ExecEnv{
		Now: func() time.Time { return now },
		Quota: func(lane string) error {
			return nil
		},
	}

	lane := "api"
	if err := env.ReportStatus(lane, 429); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// Exactly at 10*time.Minute
	now = now.Add(10 * time.Minute)
	err := env.Guard(lane)
	if err != nil {
		t.Fatalf("expected nil at exact expiry, got %v", err)
	}

	// Afterward
	now = now.Add(time.Hour)
	err = env.Guard(lane)
	if err != nil {
		t.Fatalf("expected nil after expiry, got %v", err)
	}

	// Zero timestamp and absent cooldown entry
	env2 := &ExecEnv{
		Now: func() time.Time { return time.Time{} },
		Quota: func(lane string) error {
			return nil
		},
	}
	err = env2.Guard(lane)
	if err != nil {
		t.Fatalf("expected nil for zero timestamp and absent entry, got %v", err)
	}
}
