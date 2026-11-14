package log

import (
	"testing"
	"time"
)

// p99FakeClock is a fake clock for testing Current.
type p99FakeClock struct {
	now time.Time
}

// Now returns the current fake time.
func (f *p99FakeClock) Now() time.Time {
	return f.now
}

// TestRatePublishesP99At30Samples verifies nearest-rank p99 and width clamping.
func TestRatePublishesP99At30Samples(t *testing.T) {
	fc := &p99FakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	rt, err := NewRateTracker(fc)
	if err != nil {
		t.Fatalf("NewRateTracker: %v", err)
	}

	// values 1..30 yield nearest-rank 30, width 30
	for i := 1; i <= 30; i++ {
		if err := rt.Observe(float64(i)); err != nil {
			t.Fatalf("Observe(%d): %v", i, err)
		}
	}

	rpm, width := rt.Current()
	if rpm != 30 {
		t.Errorf("rpm = %v, want 30", rpm)
	}
	if width != 30 {
		t.Errorf("width = %d, want 30", width)
	}

	// Subsequent call with no elapsed time returns same result.
	rpm2, width2 := rt.Current()
	if rpm2 != rpm || width2 != width {
		t.Errorf("second call: got (%v,%d), want (%v,%d)", rpm2, width2, rpm, width)
	}
}

// TestRatePublishesP99TinyRates verifies that 30 tiny positive rates give width 200.
func TestRatePublishesP99TinyRates(t *testing.T) {
	fc := &p99FakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	rt, err := NewRateTracker(fc)
	if err != nil {
		t.Fatalf("NewRateTracker: %v", err)
	}
	for i := 0; i < 30; i++ {
		if err := rt.Observe(0.001); err != nil {
			t.Fatalf("Observe tiny: %v", err)
		}
	}
	_, w := rt.Current()
	if w != 200 {
		t.Errorf("tiny rates width = %d, want 200", w)
	}
}

// TestRatePublishesP99LargeRates verifies that rates > 900 give width 1.
func TestRatePublishesP99LargeRates(t *testing.T) {
	fc := &p99FakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	rt, err := NewRateTracker(fc)
	if err != nil {
		t.Fatalf("NewRateTracker: %v", err)
	}
	for i := 0; i < 30; i++ {
		if err := rt.Observe(1000); err != nil {
			t.Fatalf("Observe large: %v", err)
		}
	}
	_, w := rt.Current()
	if w != 1 {
		t.Errorf("large rates width = %d, want 1", w)
	}
}

// TestRatePublishesP99At100Samples verifies that 100 rates 1..100 give p99 = 99.
func TestRatePublishesP99At100Samples(t *testing.T) {
	fc := &p99FakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	rt, err := NewRateTracker(fc)
	if err != nil {
		t.Fatalf("NewRateTracker: %v", err)
	}
	for i := 1; i <= 100; i++ {
		if err := rt.Observe(float64(i)); err != nil {
			t.Fatalf("Observe(%d): %v", i, err)
		}
	}
	rpm, _ := rt.Current()
	if rpm != 99 {
		t.Errorf("100 samples rpm = %v, want 99", rpm)
	}
}
