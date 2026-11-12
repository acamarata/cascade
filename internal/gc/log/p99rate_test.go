package log

import (
	"errors"
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	return f.now
}

func TestRateFallbackBeforeFirstPublish(t *testing.T) {
	// Test that nil clock returns ErrInvalidClock and constructor never reads Now.
	rt, err := NewRateTracker(nil)
	if err == nil || !errors.Is(err, ErrInvalidClock) {
		t.Fatalf("NewRateTracker(nil) error = %v, want ErrInvalidClock", err)
	}
	if rt != nil {
		t.Fatal("NewRateTracker(nil) returned non-nil tracker")
	}

	// Test that typed-nil clock returns ErrInvalidClock.
	var nilClock *fakeClock
	rt, err = NewRateTracker(nilClock)
	if err == nil || !errors.Is(err, ErrInvalidClock) {
		t.Fatalf("NewRateTracker(typed-nil) error = %v, want ErrInvalidClock", err)
	}
	if rt != nil {
		t.Fatal("NewRateTracker(typed-nil) returned non-nil tracker")
	}

	// Test that no samples returns rpm=10, width=90.
	fc := &fakeClock{now: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)}
	rt, err = NewRateTracker(fc)
	if err != nil {
		t.Fatalf("NewRateTracker(fakeClock) error = %v", err)
	}
	if rt == nil {
		t.Fatal("NewRateTracker(fakeClock) returned nil tracker")
	}

	rpm, width := rt.Current()
	if rpm != 10 {
		t.Errorf("Current() rpm = %v, want 10", rpm)
	}
	if width != 90 {
		t.Errorf("Current() width = %v, want 90", width)
	}
}
