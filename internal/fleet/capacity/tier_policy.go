// Purpose (this file): the ONE home of the R-16.37 jump rule and the
// reserve_tier0 flag (R-16.71) -- no other file in internal/ re-implements
// either. policy.go's SelectTier calls evaluateJump and walkOrder; nothing
// else in the tree calls the three R-16.37 triggers or the expected-time
// jump directly.
//
// Inputs: the normalized ResourceRequest, the attempt count, tier-0's
// ExpectedTime, and the best available lower tier's ExpectedTime (or
// haveLower=false when no lower tier is currently schedulable).
// Outputs: whether the jump fires and which JumpReasonCode caused it; the
// tier-walk order for a given (jumpFire, reserve) pair.
// Constraints: conditions are evaluated in the contract's declared order
// (risk_score, attempt, min_quality, expected_time) -- first match wins,
// so JumpReasonCode is deterministic when more than one condition holds.
// reserve_tier0 never suppresses a jump (R-16.71): it only removes tier-0
// from the WALK-CONTINUATION path when no jump fires.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).

package capacity

import "github.com/acamarata/cascade/internal/jobs"

// jumpTrigger is one (fire, code) evaluation.
type jumpTrigger struct {
	fire bool
	code JumpReasonCode
}

// evaluateJump evaluates the R-16.37 jump-rule conditions plus the
// R-21.175 expected-time rule, in the contract's declared order, and
// returns the first that fires. tier0Score is tier-0's capability score
// for req's task class (already panic/NaN-recovered by the caller).
func evaluateJump(req ResourceRequest, attempt int, tier0Score float64, et0 ExpectedTime, bestLower ExpectedTime, haveLower bool) jumpTrigger {
	if riskScoreJumpFires(req.RiskClass, tier0Score) {
		return jumpTrigger{true, JumpReasonRiskScore}
	}
	if attempt >= 2 {
		return jumpTrigger{true, JumpReasonAttempt}
	}
	if req.MinQuality == QualityMax {
		return jumpTrigger{true, JumpReasonMinQuality}
	}
	if haveLower && JumpFires(et0, bestLower) {
		return jumpTrigger{true, JumpReasonExpectedTime}
	}
	return jumpTrigger{false, JumpReasonNone}
}

// riskScoreJumpFires implements jump condition (a): req.risk_class is
// High or Critical AND the tier-0 capability score for this task class is
// below 0.70.
func riskScoreJumpFires(risk jobs.RiskClass, tier0Score float64) bool {
	if risk != jobs.RiskClassHigh && risk != jobs.RiskClassCritical {
		return false
	}
	return tier0Score < 0.70
}

// walkOrder returns the tier-walk priority order for one SelectTier call.
// When jumpFire is true, tier-0 is tried first (the jump), then the
// normal tier-2 -> tier-1 fallback if tier-0 is itself unschedulable.
// When jumpFire is false and reserve is true, tier-0 is removed from the
// walk entirely (reserved, never used as ordinary overflow). Otherwise
// the plain tier-2 -> tier-1 -> tier-0 walk applies.
func walkOrder(jumpFire, reserve bool) []Tier {
	switch {
	case jumpFire:
		return []Tier{TierZero, TierTwo, TierOne}
	case reserve:
		return []Tier{TierTwo, TierOne}
	default:
		return []Tier{TierTwo, TierOne, TierZero}
	}
}
