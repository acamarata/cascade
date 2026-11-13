package crunch

import (
	"errors"
	"testing"
	"time"
)

// guardFixture is an ExecEnv whose Quota hook counts calls and records the lane.
type guardFixture struct {
	now     time.Time
	calls   int
	laneArg string
	env     *ExecEnv
}

func newGuardFixture() *guardFixture {
	f := &guardFixture{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	f.env = &ExecEnv{
		Now: func() time.Time { return f.now },
		Quota: func(lane string) error {
			f.calls++
			f.laneArg = lane
			return nil
		},
	}
	return f
}

func TestQuotaFloorCheckedPreAdmission(t *testing.T) {
	f := newGuardFixture()

	// Test valid lane, quota admits
	err := f.env.Guard("test-lane")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 quota call, got %d", f.calls)
	}
	if f.laneArg != "test-lane" {
		t.Fatalf("expected lane 'test-lane', got %q", f.laneArg)
	}
}

func TestQuotaFloorCooldownStillCallsQuota(t *testing.T) {
	f := newGuardFixture()

	// Test cooldown denial still calls Quota exactly once
	if err := f.env.ReportStatus("test-lane", 429); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	err := f.env.Guard("test-lane")
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown, got %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 quota call during cooldown, got %d", f.calls)
	}

	// Test after cooldown expires (exactly 10 minutes later should still be in cooldown if strictly Before, let's add a second)
	f.now = f.now.Add(10 * time.Minute).Add(time.Second)
	f.calls = 0
	err = f.env.Guard("test-lane")
	if err != nil {
		t.Fatalf("expected nil err after cooldown, got %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 quota call after cooldown, got %d", f.calls)
	}
}

func TestQuotaFloorErrorWrapsCause(t *testing.T) {
	f := newGuardFixture()

	// Test Quota error wraps ErrQuotaFloor and cause
	underlying := errors.New("underlying error")
	env2 := &ExecEnv{
		Now: func() time.Time { return f.now },
		Quota: func(_ string) error {
			f.calls++
			return underlying
		},
	}
	err := env2.Guard("test-lane")
	if !errors.Is(err, ErrQuotaFloor) {
		t.Fatalf("expected error to wrap ErrQuotaFloor, got %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("expected error to wrap underlying error, got %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 quota call on error, got %d", f.calls)
	}
}

func TestQuotaFloorInvalidGuardInputs(t *testing.T) {
	f := newGuardFixture()
	now := f.now

	invalidCases := []struct {
		name string
		env  *ExecEnv
		lane string
	}{
		{"nil env", nil, "lane"},
		{"empty lane", f.env, ""},
		{"nil Now", &ExecEnv{Quota: func(string) error { return nil }}, "lane"},
		{"nil Quota", &ExecEnv{Now: func() time.Time { return now }}, "lane"},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			callsBefore := f.calls
			err := tc.env.Guard(tc.lane)
			if !errors.Is(err, ErrInvalidGuard) {
				t.Fatalf("expected ErrInvalidGuard, got %v", err)
			}
			if f.calls != callsBefore {
				t.Fatalf("expected no quota calls for invalid guard, got %d", f.calls-callsBefore)
			}
		})
	}
}
