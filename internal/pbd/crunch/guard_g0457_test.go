package crunch

import (
	"errors"
	"testing"
	"time"
)

func TestQuotaFloorCheckedPreAdmission(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	var calls int
	var laneArg string

	env := &ExecEnv{
		Now: func() time.Time { return now },
		Quota: func(lane string) error {
			calls++
			laneArg = lane
			return nil
		},
	}

	// Test valid lane, quota admits
	err := env.Guard("test-lane")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 quota call, got %d", calls)
	}
	if laneArg != "test-lane" {
		t.Fatalf("expected lane 'test-lane', got %q", laneArg)
	}

	// Test cooldown denial still calls Quota exactly once
	env.ReportStatus("test-lane", 429)
	calls = 0
	err = env.Guard("test-lane")
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 quota call during cooldown, got %d", calls)
	}

	// Test after cooldown expires (exactly 10 minutes later should still be in cooldown if strictly Before, let's add a second)
	now = now.Add(10 * time.Minute).Add(time.Second)
	calls = 0
	err = env.Guard("test-lane")
	if err != nil {
		t.Fatalf("expected nil err after cooldown, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 quota call after cooldown, got %d", calls)
	}

	// Test Quota error wraps ErrQuotaFloor and cause
	underlying := errors.New("underlying error")
	env2 := &ExecEnv{
		Now: func() time.Time { return now },
		Quota: func(lane string) error {
			calls++
			return underlying
		},
	}
	calls = 0
	err = env2.Guard("test-lane")
	if !errors.Is(err, ErrQuotaFloor) {
		t.Fatalf("expected error to wrap ErrQuotaFloor, got %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("expected error to wrap underlying error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 quota call on error, got %d", calls)
	}

	// Test invalid guard inputs
	invalidCases := []struct {
		name string
		env  *ExecEnv
		lane string
	}{
		{"nil env", nil, "lane"},
		{"empty lane", env, ""},
		{"nil Now", &ExecEnv{Quota: func(string) error { return nil }}, "lane"},
		{"nil Quota", &ExecEnv{Now: func() time.Time { return now }}, "lane"},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			callsBefore := calls
			err := tc.env.Guard(tc.lane)
			if !errors.Is(err, ErrInvalidGuard) {
				t.Fatalf("expected ErrInvalidGuard, got %v", err)
			}
			if calls != callsBefore {
				t.Fatalf("expected no quota calls for invalid guard, got %d", calls-callsBefore)
			}
		})
	}
}
