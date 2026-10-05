package supervision

// Purpose (this file): NewStallDetector, the constructor that composes a
// Detector with the real retry and context rungs, the once-per-episode
// publisher and the session-store watch rule; plus the Poll error helpers
// that tell an expected escalation outcome from a real failure.
//
// Inputs: every collaborator, each required.
// Outputs: a *Detector whose rungs are bound to its own stall-record lookup.
// Constraints: a nil or zero argument is refused KindInvalidInput, named;
// the detector is fully built before it is returned, so no exported
// mutable setter exists for the rungs' binding.
//
// SPORT: fleet.supervision.stall-rungs/ADDED (P1-SUP-03).

import (
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errPollNotAlive is Poll's refusal while Run is not subscribed.
var errPollNotAlive = cascade.New(cascade.KindUnavailable, "stall detector is not subscribed; poll skipped")

// missingStallConfig names the first required argument that is nil or zero.
func missingStallConfig(j journal.Store, rungs RungConfig, attention *Store, requester ApprovalRequester, policy governor.EscalationPolicy, threshold time.Duration, clock runtime.Clock, pub StallPublisher) string {
	switch {
	case j == nil:
		return "journal"
	case rungs.Sessions == nil:
		return "rungs.Sessions"
	case rungs.Directives == nil:
		return "rungs.Directives"
	case rungs.BudgetTokens <= 0:
		return "rungs.BudgetTokens"
	case attention == nil:
		return "attention store"
	case requester == nil:
		return "approval requester"
	case len(policy.MaxAttempts) == 0:
		return "policy.MaxAttempts"
	case policy.RungDelay <= 0:
		return "policy.RungDelay"
	case policy.ConfidenceThreshold <= 0:
		return "policy.ConfidenceThreshold"
	case threshold <= 0:
		return "threshold"
	case clock == nil:
		return "clock"
	case pub == nil:
		return "publisher"
	}
	return ""
}

// NewStallDetector builds a Detector whose retry and context rungs are the
// state-driven satisfiers in stall_rungs.go, bound to the detector's own
// stall-record lookup. It publishes supervision.stalled once per episode
// through pub, applies the session watch rule through rungs.Sessions, and
// makes Poll refuse while Run is not subscribed. Every argument is
// required: a nil or zero one is refused with KindInvalidInput naming it.
func NewStallDetector(j journal.Store, rungs RungConfig, attention *Store, requester ApprovalRequester, policy governor.EscalationPolicy, threshold time.Duration, clock runtime.Clock, pub StallPublisher) (*Detector, error) {
	if name := missingStallConfig(j, rungs, attention, requester, policy, threshold, clock, pub); name != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "supervision: stall detector requires %s", name)
	}
	// The rungs need the detector's stall-record lookup, and NewDetector
	// needs the rungs first: build them unbound, then bind the lookup before
	// the detector is shared. No exported setter exists for this.
	retryer := &sessionRetryer{cfg: rungs}
	enricher := &sessionEnricher{cfg: rungs}
	d := NewDetector(j, retryer, enricher, attention, requester, policy, threshold, clock)
	retryer.lookup, enricher.lookup = d.lookupStallEvent, d.lookupStallEvent
	d.pub = pub
	d.sessions = rungs.Sessions
	d.requireAlive = true
	return d, nil
}

// isExpectedEscalation reports whether err is only the ladder's own
// per-session outcomes: EscalationExhausted or ErrEscalationRungFailed,
// matched by sentinel identity along the unwrap chain. errors.Is would match
// any error of the same Kind (every KindUnavailable failure), hiding real
// ones. A joined error is expected only when every part is.
func isExpectedEscalation(err error) bool {
	for err != nil {
		if err == governor.EscalationExhausted || err == governor.ErrEscalationRungFailed {
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			parts := u.Unwrap()
			for _, p := range parts {
				if !isExpectedEscalation(p) {
					return false
				}
			}
			return len(parts) > 0
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return false
		}
	}
	return false
}
