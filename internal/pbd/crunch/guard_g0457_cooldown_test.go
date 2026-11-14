package crunch

import (
	"errors"
	"testing"
	"time"
)

// expectGuard resets the call counter, runs Guard and asserts the error and that
// Quota was called exactly once.
func expectGuard(t *testing.T, env *ExecEnv, lane string, called *int, want error, label string) {
	t.Helper()
	*called = 0
	err := env.Guard(lane)
	if !errors.Is(err, want) {
		t.Fatalf("expected %v at %s, got %v", want, label, err)
	}
	if *called != 1 {
		t.Fatalf("expected Quota to be called once at %s, called %d times", label, *called)
	}
}

func TestAdmissionRejectedDuringCooldown(t *testing.T) {
	var (
		now     time.Time
		called  int
		quotaOk = true
	)

	env := &ExecEnv{
		Now: func() time.Time { return now },
		Quota: func(_ string) error {
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
	expectGuard(t, env, lane, &called, ErrCooldown, "start")

	// At 10*time.Minute - time.Nanosecond
	now = now.Add(10*time.Minute - time.Nanosecond)
	expectGuard(t, env, lane, &called, ErrCooldown, "deadline-1ns")

	// Quota failure takes precedence and preserves state
	now = now.Add(time.Nanosecond) // exactly 10*time.Minute
	quotaOk = false
	expectGuard(t, env, lane, &called, ErrQuotaFloor, "quota failure")
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
		Quota: func(_ string) error {
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
		Quota: func(_ string) error {
			return nil
		},
	}
	err = env2.Guard(lane)
	if err != nil {
		t.Fatalf("expected nil for zero timestamp and absent entry, got %v", err)
	}
}
