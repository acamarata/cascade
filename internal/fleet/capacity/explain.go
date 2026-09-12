// Purpose (this file): TierSelection, the tier-policy engine's output
// shape, its closed JumpReasonCode enum (R-21.167: persisted, never free
// text), and TierSelection.Explain(), the human-readable summary
// `cascade fleet capacity [--json]` and scheduler_decision consume.
//
// Inputs: none (pure data shape and formatting).
// Outputs: TierSelection values SelectTier (policy.go) constructs;
// Explain()'s string.
// Constraints: Explain() must be non-empty for every TierSelection
// SelectTier can return -- see policy_test.go/explain_test.go.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).

package capacity

import "fmt"

// JumpReasonCode is the R-21.167 closed enum SelectTier records on every
// TierSelection: the reason the jump rule fired, or none. This is what
// S-64.T2 persists on scheduler_decision -- never a free-text string.
type JumpReasonCode string

// The five closed JumpReasonCode members.
const (
	JumpReasonNone         JumpReasonCode = "none"
	JumpReasonRiskScore    JumpReasonCode = "risk_score"
	JumpReasonAttempt      JumpReasonCode = "attempt"
	JumpReasonMinQuality   JumpReasonCode = "min_quality"
	JumpReasonExpectedTime JumpReasonCode = "expected_time"
)

// Valid reports whether c is one of the five declared members.
func (c JumpReasonCode) Valid() bool {
	switch c {
	case JumpReasonNone, JumpReasonRiskScore, JumpReasonAttempt, JumpReasonMinQuality, JumpReasonExpectedTime:
		return true
	}
	return false
}

// TierSelection is TierPolicy.SelectTier's success output.
type TierSelection struct {
	Tier           Tier
	Reason         string
	JumpTriggered  bool
	JumpReasonCode JumpReasonCode
	ReserveApplied bool
	Probe          bool
	Estimates      map[Tier]ExpectedTime
}

// Explain returns a non-empty human-readable summary of which conditions
// fired for this selection: covers the jump-triggered path (naming the
// firing condition), the reserve-applied path, and the plain-walk path.
// All-exhausted and no-allowed-lanes are error paths (SelectTier returns
// no TierSelection then) and are summarized by their own sentinel errors,
// not by this method.
func (s TierSelection) Explain() string {
	parts := fmt.Sprintf("selected %s", s.Tier)
	if s.Probe {
		parts += " (probe)"
	}
	if s.JumpTriggered {
		parts += fmt.Sprintf("; jump triggered by %s", s.JumpReasonCode)
	} else {
		parts += "; walk order, no jump"
	}
	if s.ReserveApplied {
		parts += "; reserve_tier0 restricted tier-0 to jump-only selection"
	}
	if s.Reason != "" {
		parts += ": " + s.Reason
	}
	return parts
}
