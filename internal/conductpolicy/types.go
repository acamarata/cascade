// Package conductpolicy defines abstract processing lanes, work kinds, reasons and tiers for policy enforcement.
package conductpolicy

import "errors"

// Lane represents a processing lane.
type Lane string

const (
	// LaneBulk is the bulk processing lane.
	LaneBulk Lane = "bulk"
	// LanePremium is the premium processing lane.
	LanePremium Lane = "premium"
)

// Valid reports whether the Lane is a supported value.
func (l Lane) Valid() bool {
	switch l {
	case LaneBulk, LanePremium:
		return true
	default:
		return false
	}
}

// WorkKind represents a kind of work.
type WorkKind string

const (
	// WorkLaunch is the launch work kind.
	WorkLaunch WorkKind = "launch"
	// WorkManage is the manage work kind.
	WorkManage WorkKind = "manage"
	// WorkBulk is the bulk work kind.
	WorkBulk WorkKind = "bulk"
)

// Valid reports whether the WorkKind is a supported value.
func (k WorkKind) Valid() bool {
	switch k {
	case WorkLaunch, WorkManage, WorkBulk:
		return true
	default:
		return false
	}
}

// Reason represents a reason for routing or escalation.
type Reason string

const (
	// ReasonHardProblem is the hard problem reason.
	ReasonHardProblem Reason = "hard_problem"
	// ReasonConsult is the consult reason.
	ReasonConsult Reason = "consult"
	// ReasonAdversarialReview is the adversarial review reason.
	ReasonAdversarialReview Reason = "adversarial_review"
	// ReasonRoutine is the routine reason.
	ReasonRoutine Reason = "routine"
)

// Valid reports whether the Reason is a supported value.
func (r Reason) Valid() bool {
	switch r {
	case ReasonHardProblem, ReasonConsult, ReasonAdversarialReview, ReasonRoutine:
		return true
	default:
		return false
	}
}

// Tier represents a processing tier.
type Tier int

const (
	// TierStandard is the standard tier.
	TierStandard Tier = 0
	// TierHigh is the high tier.
	TierHigh Tier = 1
)

// Valid reports whether the Tier is a supported value.
func (x Tier) Valid() bool {
	switch x {
	case TierStandard, TierHigh:
		return true
	default:
		return false
	}
}

var (
	// ErrUnknownWorkKind indicates an unknown work kind.
	ErrUnknownWorkKind = errors.New("conductpolicy: unknown work kind")
	// ErrUnknownReason indicates an unknown reason.
	ErrUnknownReason = errors.New("conductpolicy: unknown reason")
	// ErrSameLane indicates that consensus needs two different lanes.
	ErrSameLane = errors.New("conductpolicy: consensus needs two different lanes")
	// ErrEmptyAnswer indicates an empty answer.
	ErrEmptyAnswer = errors.New("conductpolicy: empty answer")
	// ErrFanoutNeedsOverride indicates that native fan-out needs an explicit override with a reason.
	ErrFanoutNeedsOverride = errors.New("conductpolicy: native fan-out needs an explicit override with a reason")
)
