// Package testkit is the shared test toolkit every P1 package tests
// against: golden-fixture helpers (golden.go) and the clock-injection
// primitive (this file). Nothing in this package may be imported from
// non-test, shipped code — it exists purely so package tests can be
// deterministic (12-QUALITY-CONSTITUTION.md Art.7.3).
package testkit

import (
	"sync"
	"time"
)

// Purpose: a duck-typed twin of internal/runtime's Clock interface, so any
//   package (not only internal/runtime) can accept clock injection without
//   importing internal/runtime.
// Inputs: none (Clock is a zero-arg accessor interface).
// Outputs: the current instant, per the concrete implementation.
// Constraints: R-14.11 (15-T0-RULINGS-R14.md) makes internal/runtime the
//   canonical HOME of the production Clock interface (C/S-04.T1 owns
//   internal/runtime/clock.go) and names this package as the source of
//   "testkit helpers" alongside it — it does NOT ask testkit to define a
//   second, competing abstraction that domain code must choose between.
//   Because Go interfaces are structural, a *RealClock or *FrozenClock
//   value defined here already satisfies runtime.Clock (both declare only
//   `Now() time.Time`) with zero import from testkit to internal/runtime —
//   which this ticket may not edit (another agent owns it concurrently).
//   Any future package that wants clock injection can depend on
//   testkit.Clock directly instead of reaching into internal/runtime for a
//   type that has nothing runtime-specific about it. See DECISIONS in the
//   T-4 ticket journal for the full rationale.
// SPORT: build/testkit (ADD, placeholder per T-4 sport_updates).

// Clock abstracts time.Now so callers never read the wall clock directly.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// RealClock is the production Clock, backed by the real wall clock. It is
// test infrastructure by construction (12-QUALITY-CONSTITUTION.md Art.1.1
// / Art.1.4: testkit is never imported by shipped domain code); production
// entrypoints use their own package's Clock (e.g. runtime.NewSystemClock),
// not this one. RealClock exists so packages that adopt testkit.Clock
// directly have a real implementation to reach for outside tests too.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// NewRealClock returns the real-time Clock implementation.
func NewRealClock() Clock { return RealClock{} }

// FrozenClock is a deterministic Clock for tests (Art.7.3: no sleeps as
// synchronization). It never reads the wall clock; it only ever reports the
// instant it was constructed with, Set to, or Advanced to. A *FrozenClock
// is safe to pass anywhere a testkit.Clock or a structurally-identical
// Clock (e.g. runtime.Clock) is expected.
type FrozenClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFrozenClock returns a FrozenClock starting at t.
func NewFrozenClock(t time.Time) *FrozenClock {
	return &FrozenClock{now: t}
}

// Now returns the clock's current frozen instant.
func (c *FrozenClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the frozen clock forward by d and returns the new instant.
func (c *FrozenClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// Set pins the frozen clock to t and returns t.
func (c *FrozenClock) Set(t time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	return c.now
}
