package learn

// Purpose: decay.go's half-life, identity and no-amplification cases, driven
//   with explicit instants (the injected-clock shape the scorer uses).
// SPORT: internal.learn.decayWeight/TESTED (P1-CAP-03).

import (
	"math"
	"testing"
	"time"
)

// TestDecayHalfLife: an observation aged exactly 30 days weighs 0.50 within
// 1e-12, age 0 weighs 1.0, a future timestamp weighs 1.0 and never more, and
// 60 days weighs 0.25 (the half-life compounds).
func TestDecayHalfLife(t *testing.T) {
	now := newTestClock().Now()
	cases := []struct {
		name string
		last time.Time
		want float64
	}{
		{"thirty days", now.Add(-30 * 24 * time.Hour), 0.50},
		{"sixty days", now.Add(-60 * 24 * time.Hour), 0.25},
		{"age zero", now, 1.0},
		{"future timestamp", now.Add(90 * 24 * time.Hour), 1.0},
	}
	for _, tc := range cases {
		if got := decayWeight(now.Sub(tc.last)); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%s: decayWeight = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := decayed(8, now.Add(-30*24*time.Hour), now); math.Abs(got-4) > 1e-12 {
		t.Errorf("decayed(8, 30 days) = %v, want 4", got)
	}
	if got := decayed(8, now.Add(time.Hour), now); got != 8 {
		t.Errorf("decayed(8, future) = %v, want 8 (never amplified)", got)
	}
}
