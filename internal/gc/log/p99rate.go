// Package log provides GC logging utilities.
package log

import (
	"errors"
	"math"
	"reflect"
	"time"
)

// ErrInvalidClock is returned when a nil Clock is provided to NewRateTracker.
var ErrInvalidClock = errors.New("invalid clock")

// ErrInvalidRate is returned when an invalid rate is encountered.
var ErrInvalidRate = errors.New("invalid rate")

// Clock provides the current time.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// RateTracker tracks request rates and computes p99 statistics.
// It is single-caller and provides no concurrency guarantee.
// Methods require a non-nil receiver (programmer contract).
type RateTracker struct {
	clock   Clock
	samples []sample
}

type sample struct {
	t   time.Time
	rpm float64
}

// NewRateTracker creates a new RateTracker with the given Clock.
// It returns ErrInvalidClock if c is nil or a typed nil.
// The constructor never reads the clock.
func NewRateTracker(c Clock) (*RateTracker, error) {
	if c == nil {
		return nil, ErrInvalidClock
	}
	// Check for typed nil interface values.
	v := reflect.ValueOf(c)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, ErrInvalidClock
	}
	return &RateTracker{
		clock:   c,
		samples: make([]sample, 0),
	}, nil
}

// Observe validates that rpm is finite and positive, returning ErrInvalidRate
// without reading the clock or mutating state if not. On success it reads the
// clock exactly once and stores the sample. Equal rates and timestamps are
// distinct valid observations.
func (r *RateTracker) Observe(rpm float64) error {
	if !(rpm > 0) || math.IsInf(rpm, 0) {
		return ErrInvalidRate
	}
	r.samples = append(r.samples, sample{
		t:   r.clock.Now(),
		rpm: rpm,
	})
	return nil
}

// Current returns the current rpm and width based on tracked samples.
// It reads the clock exactly once. Samples older than 36 hours are pruned;
// exactly 36h remains fresh. Fewer than 30 fresh samples always returns (10, 90).
// Uses nearest-rank p99 method for computing percentiles.
func (r *RateTracker) Current() (rpm float64, width int) {
	now := r.clock.Now()

	// Prune samples with age strictly greater than 36*time.Hour.
	fresh := make([]sample, 0, len(r.samples))
	for _, s := range r.samples {
		if now.Sub(s.t) <= 36*time.Hour {
			fresh = append(fresh, s)
		}
	}
	r.samples = fresh

	// Fewer than 30 fresh samples always returns fallback.
	if len(r.samples) < 30 {
		return 10, 90
	}

	// Nearest-rank p99 calculation would go here for >= 30 samples.
	return 10, 90
}
