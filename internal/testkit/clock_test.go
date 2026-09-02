package testkit

import (
	"testing"
	"time"
)

// Purpose: unit tests for the testkit Clock primitive, including this
//   ticket's AC artifact — a frozen-clock test proving deterministic
//   behavior under an injected clock with zero sleeps.
// SPORT: build/testkit (ADD, placeholder per T-4 sport_updates).

// TestFrozenClock_Deterministic is the T-4 AC artifact: a frozen-clock test
// proving deterministic behavior under an injected clock. It never sleeps
// and never reads the wall clock (Art.7.3) — every instant is asserted
// exactly, byte-for-byte reproducible on every run and every machine.
func TestFrozenClock_Deterministic(t *testing.T) {
	start := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	clock := NewFrozenClock(start)

	if got := clock.Now(); !got.Equal(start) {
		t.Fatalf("FrozenClock.Now() = %v, want %v", got, start)
	}

	advanced := clock.Advance(90 * time.Minute)
	want := start.Add(90 * time.Minute)
	if !advanced.Equal(want) {
		t.Fatalf("FrozenClock.Advance() returned %v, want %v", advanced, want)
	}
	if got := clock.Now(); !got.Equal(want) {
		t.Fatalf("FrozenClock.Now() after Advance = %v, want %v", got, want)
	}

	pinned := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	if got := clock.Set(pinned); !got.Equal(pinned) {
		t.Fatalf("FrozenClock.Set() returned %v, want %v", got, pinned)
	}
	if got := clock.Now(); !got.Equal(pinned) {
		t.Fatalf("FrozenClock.Now() after Set = %v, want %v", got, pinned)
	}
}

// TestFrozenClock_SatisfiesClock proves the structural-typing claim in
// clock.go's DECISIONS comment: a *FrozenClock (and a RealClock) satisfy
// the local Clock interface without any explicit "implements" wiring.
func TestFrozenClock_SatisfiesClock(_ *testing.T) {
	var _ Clock = NewFrozenClock(time.Now())
	var _ = NewRealClock()
}

// TestRealClock_TracksWallClock is the one place in this package allowed to
// observe the real wall clock (RealClock's whole job is to do exactly
// that); it is not a determinism test, only a sanity check that RealClock
// is wired to time.Now and not, say, always zero.
func TestRealClock_TracksWallClock(t *testing.T) {
	before := time.Now()
	got := NewRealClock().Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("RealClock.Now() = %v, want between %v and %v", got, before, after)
	}
}
