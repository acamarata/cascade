package runtime

import "time"

// Purpose: injectable time source for every internal/runtime domain type.
// Inputs: none (Clock is a zero-arg accessor interface).
// Outputs: the current instant, per the concrete Clock implementation.
// Constraints: 02-TARGET-STRUCTURE §v1.1 forbids bare time.Now() in domain
//   logic; R-14.11 makes internal/runtime the canonical home for this
//   interface (no separate pkg/clock). Tests must use FixedClock, never the
//   system clock, to stay deterministic (Art.7.3).
// SPORT: runtime/clock (ADD, placeholder per T-1 sport_updates).

// Clock abstracts time.Now so callers never read the wall clock directly.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// SystemClock is the production Clock, backed by the real wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// NewSystemClock returns the production Clock implementation. Only
// production entrypoints (bootstrap.go and above) should call this; tests
// must inject FixedClock instead.
func NewSystemClock() Clock { return SystemClock{} }

// FixedClock is a deterministic Clock for tests. It never reads the real
// wall clock; it only ever reports the instant it was constructed with or
// last advanced to.
type FixedClock struct {
	now time.Time
}

// NewFixedClock returns a FixedClock starting at t.
func NewFixedClock(t time.Time) *FixedClock {
	return &FixedClock{now: t}
}

// Now returns the clock's current fixed instant.
func (c *FixedClock) Now() time.Time { return c.now }

// Advance moves the fixed clock forward by d and returns the new instant.
func (c *FixedClock) Advance(d time.Duration) time.Time {
	c.now = c.now.Add(d)
	return c.now
}
