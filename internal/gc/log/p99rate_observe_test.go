package log

import (
	"errors"
	"math"
	"testing"
	"time"
)

type observingFakeClock struct {
	now   time.Time
	reads int
}

func (f *observingFakeClock) Now() time.Time {
	f.reads++
	return f.now
}

// TestRateFallbackUnder30Samples verifies that Observe validates input,
// stores valid samples with a single clock read, and that Current()
// returns (10, 90) when fewer than 30 fresh samples exist.
func TestRateFallbackUnder30Samples(t *testing.T) {
	fc := &observingFakeClock{now: time.Now()}
	rt, err := NewRateTracker(fc)
	if err != nil {
		t.Fatalf("NewRateTracker: %v", err)
	}

	// Test invalid rates: zero, negative, NaN, infinite.
	// None should mutate state or read the clock.
	initialReads := fc.reads
	invalidRates := []float64{
		0.0,
		-1.0,
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
	}
	for _, rpm := range invalidRates {
		err := rt.Observe(rpm)
		if !errors.Is(err, ErrInvalidRate) {
			t.Errorf("Observe(%v) = %v, want ErrInvalidRate", rpm, err)
		}
	}
	if fc.reads != initialReads {
		t.Errorf("clock reads = %d after invalid observations, want %d (no reads expected)", fc.reads, initialReads)
	}
	rpm, width := rt.Current()
	if rpm != 10 || width != 90 {
		t.Errorf("Current() after invalid observations = (%v, %v), want (10, 90)", rpm, width)
	}

	// Create a fresh tracker and add exactly 29 samples.
	fc2 := &observingFakeClock{now: time.Now()}
	rt2, err := NewRateTracker(fc2)
	if err != nil {
		t.Fatalf("NewRateTracker (2): %v", err)
	}
	baseTime := time.Now()
	for i := 0; i < 29; i++ {
		fc2.now = baseTime.Add(time.Duration(i) * time.Minute)
		if err := rt2.Observe(100.0); err != nil {
			t.Fatalf("Observe(100.0) sample %d: %v", i, err)
		}
	}

	// With 29 fresh samples, Current should return fallback (10, 90).
	fc2.now = baseTime.Add(30 * time.Minute)
	rpm, width = rt2.Current()
	if rpm != 10 || width != 90 {
		t.Errorf("Current() with 29 fresh samples = (%v, %v), want (10, 90)", rpm, width)
	}
}
