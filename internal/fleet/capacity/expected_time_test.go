// Purpose: TestExpectedTimeFormula asserts ComputeExpectedTime against
// hand-computed values (never against its own formula restated), and
// TestTierPolicyJumpHysteresis (moved here for its formula-boundary
// cases; the walk-integrated case lives in tier_policy_test.go) asserts
// JumpFires' exact 0.8 boundary.
//
// SPORT: fleet.capacity.expected_time (ADD, P1-E31-W6-S63-T2).
package capacity

import (
	"testing"
	"time"
)

// TestExpectedTimeFormula hand-computes expected_time = queue_wait +
// duration_est * (1 + p_rework * rework_cycles_est) independently of
// ComputeExpectedTime's own code path.
func TestExpectedTimeFormula(t *testing.T) {
	cases := []struct {
		name        string
		queueWait   time.Duration
		durationEst time.Duration
		score       float64
	}{
		{"score 0.90", 0, 10 * time.Minute, 0.90},
		{"score 0.70 with queue wait", 2 * time.Minute, 10 * time.Minute, 0.70},
		{"score 0.0 (unknown lane)", 0, 5 * time.Minute, 0.0},
		{"score 1.0 (no rework)", time.Minute, 20 * time.Minute, 1.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pRework := 1 - c.score
			wantFactor := 1 + pRework*float64(ReworkCyclesEst)
			wantExpected := c.queueWait + time.Duration(float64(c.durationEst)*wantFactor)

			got := ComputeExpectedTime(c.queueWait, c.durationEst, c.score)
			if got.PRework != pRework {
				t.Errorf("PRework = %v, want %v", got.PRework, pRework)
			}
			if got.ReworkCyclesEst != ReworkCyclesEst {
				t.Errorf("ReworkCyclesEst = %v, want the priors.go constant %v", got.ReworkCyclesEst, ReworkCyclesEst)
			}
			if got.ExpectedTime != wantExpected {
				t.Errorf("ExpectedTime = %v, want %v (hand-computed)", got.ExpectedTime, wantExpected)
			}
		})
	}
}

// TestJumpFiresHysteresisBoundary asserts JumpFires' exact 0.8 margin: no
// jump AT the boundary, a jump strictly below it.
func TestJumpFiresHysteresisBoundary(t *testing.T) {
	bestLower := ExpectedTime{ExpectedTime: 100 * time.Second}

	atBoundary := ExpectedTime{ExpectedTime: time.Duration(JumpHysteresisFactor * float64(bestLower.ExpectedTime))}
	if JumpFires(atBoundary, bestLower) {
		t.Fatalf("JumpFires at exactly the %v hysteresis boundary = true, want false", JumpHysteresisFactor)
	}

	belowBoundary := ExpectedTime{ExpectedTime: atBoundary.ExpectedTime - time.Second}
	if !JumpFires(belowBoundary, bestLower) {
		t.Fatal("JumpFires strictly below the hysteresis boundary = false, want true")
	}

	aboveBoundary := ExpectedTime{ExpectedTime: atBoundary.ExpectedTime + time.Second}
	if JumpFires(aboveBoundary, bestLower) {
		t.Fatal("JumpFires above the hysteresis boundary = true, want false")
	}
}
