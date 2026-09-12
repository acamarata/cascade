// Purpose: TestTierPolicyJumpRule (all four conditions, individually and
// in combination) and TestTierPolicyJumpHysteresis (the walk-integrated
// cold-start/no-oscillation cases; the pure formula-boundary cases live
// in expected_time_test.go's TestJumpFiresHysteresisBoundary) -- this
// file's two named acceptance tests, asserted against tier_policy.go's
// evaluateJump/walkOrder directly, never against a second restatement of
// R-16.37's own conditions.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).
package capacity

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/jobs"
)

// TestTierPolicyJumpRule asserts each of the four R-16.37/R-21.175 jump
// conditions individually, that they are independent (any one suffices),
// and that evaluateJump's declared precedence order picks the first that
// fires when more than one holds.
// jumpRuleCase is one evaluateJump table row.
type jumpRuleCase struct {
	name       string
	req        ResourceRequest
	attempt    int
	tier0Score float64
	et0        ExpectedTime
	bestLower  ExpectedTime
	haveLower  bool
	wantFire   bool
	wantCode   JumpReasonCode
}

// jumpRuleCases is split out of TestTierPolicyJumpRule so the test
// function itself stays under the 50-line cap.
func jumpRuleCases(req ResourceRequest) []jumpRuleCase {
	et0Low := ExpectedTime{ExpectedTime: 10 * time.Second}
	etLowerHigh := ExpectedTime{ExpectedTime: time.Hour}
	etLowerLow := ExpectedTime{ExpectedTime: 11 * time.Second}
	return []jumpRuleCase{
		{name: "a: high risk and low tier-0 score", req: withRisk(req, jobs.RiskClassHigh),
			tier0Score: 0.5, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: true, wantCode: JumpReasonRiskScore},
		{name: "a does not fire: critical risk but score >= 0.70", req: withRisk(req, jobs.RiskClassCritical),
			tier0Score: 0.70, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: false, wantCode: JumpReasonNone},
		{name: "a does not fire: low score but risk normal", req: withRisk(req, jobs.RiskClassNormal),
			tier0Score: 0.1, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: false, wantCode: JumpReasonNone},
		{name: "b: attempt >= 2", req: req, attempt: 2,
			tier0Score: 0.9, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: true, wantCode: JumpReasonAttempt},
		{name: "b does not fire below 2", req: req, attempt: 1,
			tier0Score: 0.9, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: false, wantCode: JumpReasonNone},
		{name: "c: min_quality == max", req: withQuality(req, QualityMax),
			tier0Score: 0.9, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: true, wantCode: JumpReasonMinQuality},
		{name: "d: expected-time jump fires below hysteresis", req: req,
			tier0Score: 0.9, et0: et0Low, bestLower: etLowerHigh, haveLower: true, wantFire: true, wantCode: JumpReasonExpectedTime},
		{name: "d does not fire: no lower tier available (cold start with nothing to compare)", req: req,
			tier0Score: 0.9, et0: et0Low, haveLower: false, wantFire: false, wantCode: JumpReasonNone},
		{name: "combination: a and b both hold -- a (declared first) wins", req: withRisk(req, jobs.RiskClassCritical), attempt: 2,
			tier0Score: 0.1, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: true, wantCode: JumpReasonRiskScore},
		{name: "combination: b and c both hold -- b (declared before c) wins", req: withQuality(req, QualityMax), attempt: 2,
			tier0Score: 0.9, et0: etLowerHigh, bestLower: etLowerHigh, haveLower: true, wantFire: true, wantCode: JumpReasonAttempt},
		{name: "none fire", req: req,
			tier0Score: 0.9, et0: etLowerHigh, bestLower: etLowerLow, haveLower: true, wantFire: false, wantCode: JumpReasonNone},
	}
}

func TestTierPolicyJumpRule(t *testing.T) {
	req := baseTestRequest()
	for _, c := range jumpRuleCases(req) {
		t.Run(c.name, func(t *testing.T) {
			got := evaluateJump(c.req, c.attempt, c.tier0Score, c.et0, c.bestLower, c.haveLower)
			if got.fire != c.wantFire || got.code != c.wantCode {
				t.Fatalf("evaluateJump = (fire=%v, code=%v), want (fire=%v, code=%v)", got.fire, got.code, c.wantFire, c.wantCode)
			}
		})
	}
}

func withRisk(req ResourceRequest, r jobs.RiskClass) ResourceRequest {
	req.RiskClass = r
	return req
}

func withQuality(req ResourceRequest, q QualityEnum) ResourceRequest {
	req.MinQuality = q
	return req
}

// TestTierPolicyJumpHysteresis asserts the walk-integrated cold-start
// case (no observations: the comparison reduces to the S-63.T4 priors
// alone) and no oscillation across two consecutive, unchanged-input
// SelectTier calls.
func TestTierPolicyJumpHysteresis(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	// Cold start: EstimateSource returns queue_wait=0 and one identical
	// duration_est for every tier -- stubEstimator's default already does
	// this via coldDuration.
	est := stubEstimator{coldDuration: 10 * time.Minute}
	req := baseTestRequest()
	allowed := []conductor.LaneID{"lane-a"}
	slots := map[Tier]TierSlot{
		TierTwo:  availableSlot("p2"),
		TierOne:  availableSlot("p1"),
		TierZero: availableSlot("p0"),
	}

	// Cold start reduces to: (1 + p_rework(tier0)) < 0.8*(1 + p_rework(tier1
	// or tier2, whichever scores lower and thus wins as "bestLower" by
	// LOWER expected_time -- lower p_rework means lower expected_time, so
	// bestLower is whichever of tier-1/tier-2 has the HIGHER capability
	// score). Using S-63.T4's priors directly for TaskClassCode:
	// tier0=0.90, tier1=0.85, tier2=0.75 -> bestLower is tier-1 (0.85,
	// lower p_rework than tier-2's 0.75).
	scorer := stubScorer{scores: map[Tier]float64{
		TierZero: Priors[TierZero][conductor.TaskClassCode],
		TierOne:  Priors[TierOne][conductor.TaskClassCode],
		TierTwo:  Priors[TierTwo][conductor.TaskClassCode],
	}}

	p := newTestPolicy(est, clk)
	first, err := p.SelectTier(slots, allowed, req, scorer, 0)
	if err != nil {
		t.Fatalf("SelectTier: %v", err)
	}
	wantP0 := PReworkFromScore(Priors[TierZero][conductor.TaskClassCode])
	wantP1 := PReworkFromScore(Priors[TierOne][conductor.TaskClassCode])
	wantFire := (1 + wantP0) < JumpHysteresisFactor*(1+wantP1)
	if first.JumpTriggered != wantFire {
		t.Fatalf("cold-start JumpTriggered = %v, want %v (hand-computed from priors alone)", first.JumpTriggered, wantFire)
	}

	// No oscillation: an identical second call on unchanged inputs
	// produces the identical decision.
	second, err := p.SelectTier(slots, allowed, req, scorer, 0)
	if err != nil {
		t.Fatalf("SelectTier (second call): %v", err)
	}
	if second.Tier != first.Tier || second.JumpTriggered != first.JumpTriggered || second.JumpReasonCode != first.JumpReasonCode {
		t.Fatalf("second call = %+v, want identical to first = %+v (no oscillation)", second, first)
	}
}
