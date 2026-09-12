// Purpose (this file): the three interfaces TierPolicy.SelectTier
// injects (CapabilityScorer, EstimateSource, ProbeAdmission), TierSlot
// (this ticket's own minimal per-tier input shape -- see the CONTRACT
// DEVIATION note below), and SelectTier itself: the tier walk plus
// user-config lane filtering. The four jump-rule conditions and the
// reserve_tier0 flag live in tier_policy.go, their ONE home (R-16.71);
// this file calls evaluateJump and walkOrder rather than re-implementing
// either.
//
// CONTRACT DEVIATION (recorded, not papered over). The contract's
// full_desc names SelectTier's shape as "SelectTier(snap, req, scorer,
// attempt)", implying the S-63.T1 FleetSnapshot as the first parameter.
// FleetSnapshot (snapshot.go, this package) has no profile-to-tier
// binding yet -- ProviderSlot carries no Tier field, and task 3 of this
// ticket's own contract limits the injected interfaces to exactly three
// ("Declare the three injected interfaces in policy.go and nothing
// else"), which forbids adding a fourth "resolve snapshot to tier"
// interface to bridge that gap. SelectTier therefore takes a pre-resolved
// map[Tier]TierSlot instead of the raw FleetSnapshot: this ticket's own
// minimal shape for "what a tier's capacity currently looks like",
// leaving the FleetSnapshot -> map[Tier]TierSlot projection (which
// requires the config-domain profile/tier binding this ticket's
// dependencies do not yet ship) to the future ticket that wires a live
// caller, exactly as priors.go recorded for its own unconsumed exports.
//
// Inputs: a map[Tier]TierSlot (the resolved per-tier capacity view), the
// caller's allowed-lane list (nil means absent/unresolvable, R-16.11 task
// 4), a normalized ResourceRequest, a CapabilityScorer, and the attempt
// count.
// Outputs: a TierSelection on success; ErrFleetExhausted or
// ErrNoAllowedLanes on failure.
// Constraints: fail-closed throughout -- an unknown lane, an unparseable
// policy row, or a missing reserve flag never falls through to the most
// permissive tier; a scorer panic or NaN never propagates to the caller.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).

package capacity

import (
	"math"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
)

// CapabilityScorer scores one (tier, task_class) pair. The priors DATA
// table is owned by S-63.T4 (priors.go) and the Bayesian scoring
// implementation by S-64.T2 (internal/learn, R-16.71); this ticket ships
// no priors values and no scorer implementation -- callers inject one. An
// unknown lane or task class must score 0.0, the most-restrictive value.
type CapabilityScorer interface {
	Score(tier Tier, tc conductor.TaskClass) float64
}

// EstimateSource returns queue_wait and duration_est for one (tier,
// task_class) pair, the R-21.175 formula's two inputs. A lane with no
// observations returns the cold-start values: queue_wait = 0 and one
// identical duration_est across every tier queried this way, per
// EstimateSource's contract (implemented over the S-64.T2 observed
// aggregates -- this ticket declares only the interface).
type EstimateSource interface {
	Estimate(tier Tier, tc conductor.TaskClass) (queueWait, durationEst time.Duration)
}

// ProbeAdmission gates an `unknown` slot's probe-only admission
// (R-21.166). TryAdmit reports whether profile may be scheduled now
// (false inside its backoff window); when dispatchProbe is true it
// additionally enforces at most one in-flight probe per profile. Classify
// records a probe's outcome and opens/clears the backoff. probe.go ships
// this ticket's own DefaultProbeAdmission implementation.
type ProbeAdmission interface {
	TryAdmit(profile string, dispatchProbe bool, now time.Time) bool
	Classify(profile string, outcome State, now time.Time)
}

// TierSlot is one tier's resolved capacity view for a single SelectTier
// call.
type TierSlot struct {
	// ProfileID keys ProbeAdmission's per-profile backoff/in-flight state.
	ProfileID string
	// Providers is the lane ids currently backing this tier, for the
	// user-config allowed-lane filter (task 4).
	Providers []conductor.LaneID
	State     State
	Source    ObservationSource
	// ObservedAt is when State was last observed, for staleness degrade.
	ObservedAt time.Time
}

// resolvedSlot is one tier's state after staleness degrade, probe
// admission, and lane filtering -- SelectTier's own working value, never
// exported.
type resolvedSlot struct {
	state      State
	probe      bool
	disallowed bool
}

// TierPolicy is the tier-policy engine. Every dependency is injected at
// construction; SelectTier itself takes only the per-call inputs that
// genuinely vary per request (slots, allowed lanes, req, scorer, attempt).
type TierPolicy struct {
	Estimator EstimateSource
	Probes    ProbeAdmission
	Clock     Clock
}

// NewTierPolicy constructs a TierPolicy from its three injected
// dependencies.
func NewTierPolicy(estimator EstimateSource, probes ProbeAdmission, clock Clock) *TierPolicy {
	return &TierPolicy{Estimator: estimator, Probes: probes, Clock: clock}
}

// walkTiers is the fixed set SelectTier resolves per call, independent of
// walk order (walkOrder, tier_policy.go, decides priority; this decides
// membership).
var walkTiers = []Tier{TierTwo, TierOne, TierZero}

// anyLaneAllowed reports whether any of providers appears in allowed.
func anyLaneAllowed(allowed, providers []conductor.LaneID) bool {
	set := make(map[conductor.LaneID]bool, len(allowed))
	for _, l := range allowed {
		set[l] = true
	}
	for _, p := range providers {
		if set[p] {
			return true
		}
	}
	return false
}

// schedulable reports whether state admits normal scheduling (available
// or constrained). exhausted, auth_required, and unresolved unknown are
// never schedulable.
func schedulable(state State) bool {
	return state == StateAvailable || state == StateConstrained
}

// resolveSlot applies staleness degrade, probe admission, and lane
// filtering to one tier's raw TierSlot, in that order.
func (p *TierPolicy) resolveSlot(tier Tier, slot TierSlot, allowedLanes []conductor.LaneID, now time.Time) resolvedSlot {
	st := DegradeStaleness(slot.State, slot.Source, slot.ObservedAt, now)
	probe := false
	if st == StateUnknown {
		if p.Probes.TryAdmit(slot.ProfileID, true, now) {
			st, probe = StateAvailable, true
		} else {
			st = StateExhausted
		}
	} else if !p.Probes.TryAdmit(slot.ProfileID, false, now) {
		st = StateExhausted
	}
	// disallowed only tracks the cases where the LANE POLICY is what turns
	// a tier unschedulable -- a tier already exhausted for a raw capacity
	// reason before this filter runs is not "disallowed", so allDisallowed
	// (used to choose ErrNoAllowedLanes vs ErrFleetExhausted) is not
	// confused by a tier that failed for both reasons at once.
	disallowed := false
	switch {
	case allowedLanes == nil:
		if tier == TierZero && schedulable(st) {
			st, disallowed = StateExhausted, true
		}
	case schedulable(st) && !anyLaneAllowed(allowedLanes, slot.Providers):
		st, disallowed = StateExhausted, true
	}
	return resolvedSlot{state: st, probe: probe, disallowed: disallowed}
}

// scoreSafe calls scorer.Score, recovering a panic and treating a NaN
// result as 0.0 (the most-restrictive value) -- a scorer's misbehavior
// never propagates to the caller (task 4 error path).
func scoreSafe(scorer CapabilityScorer, tier Tier, tc conductor.TaskClass) (s float64) {
	defer func() {
		if recover() != nil {
			s = 0
		}
	}()
	s = scorer.Score(tier, tc)
	if math.IsNaN(s) {
		return 0
	}
	return s
}

// estimateFor computes tier's ExpectedTime from p's EstimateSource and
// scorer's score, recovering a scorer panic the same way scoreSafe does.
func (p *TierPolicy) estimateFor(tier Tier, tc conductor.TaskClass, scorer CapabilityScorer) ExpectedTime {
	score := scoreSafe(scorer, tier, tc)
	qw, de := p.Estimator.Estimate(tier, tc)
	return ComputeExpectedTime(qw, de, score)
}

// bestLower scans tier-1 and tier-2 for the lowest expected_time among
// tiers resolved as currently schedulable, for the R-21.175 jump
// comparison. Returns the winning tier alongside its ExpectedTime so the
// caller can key its own estimates map without a second scan.
func (p *TierPolicy) bestLower(resolved map[Tier]resolvedSlot, tc conductor.TaskClass, scorer CapabilityScorer) (Tier, ExpectedTime, bool) {
	var winner Tier
	var best ExpectedTime
	have := false
	for _, tier := range []Tier{TierOne, TierTwo} {
		if !schedulable(resolved[tier].state) {
			continue
		}
		et := p.estimateFor(tier, tc, scorer)
		if !have || et.ExpectedTime < best.ExpectedTime {
			best, winner, have = et, tier, true
		}
	}
	return winner, best, have
}

// allDisallowed reports whether every tier in order was excluded by the
// user-config lane filter (never by raw unavailability) -- the exact
// distinction ErrNoAllowedLanes vs ErrFleetExhausted turns on.
func allDisallowed(resolved map[Tier]resolvedSlot, order []Tier) bool {
	for _, t := range order {
		if !resolved[t].disallowed {
			return false
		}
	}
	return true
}

// SelectTier walks tier-2 -> tier-1 -> tier-0 by availability, applying
// the R-16.37 jump rule and the reserve_tier0 flag (tier_policy.go), and
// the R-21.166 probe/staleness rules and R-16.11 lane filtering (above).
func (p *TierPolicy) SelectTier(slots map[Tier]TierSlot, allowedLanes []conductor.LaneID, req ResourceRequest, scorer CapabilityScorer, attempt int) (TierSelection, error) {
	now := p.Clock.Now()
	resolved := make(map[Tier]resolvedSlot, len(walkTiers))
	for _, tier := range walkTiers {
		resolved[tier] = p.resolveSlot(tier, slots[tier], allowedLanes, now)
	}

	tier0Score := scoreSafe(scorer, TierZero, req.TaskClass)
	et0 := p.estimateFor(TierZero, req.TaskClass, scorer)
	lowerTier, bestLower, haveLower := p.bestLower(resolved, req.TaskClass, scorer)

	jump := evaluateJump(req, attempt, tier0Score, et0, bestLower, haveLower)
	order := walkOrder(jump.fire, req.ReserveTier0)

	estimates := map[Tier]ExpectedTime{TierZero: et0}
	if haveLower {
		estimates[lowerTier] = bestLower
	}

	for _, tier := range order {
		rs := resolved[tier]
		if !schedulable(rs.state) {
			continue
		}
		return TierSelection{
			Tier:           tier,
			JumpTriggered:  jump.fire,
			JumpReasonCode: jump.code,
			ReserveApplied: req.ReserveTier0,
			Probe:          rs.probe,
			Estimates:      estimates,
		}, nil
	}

	if allDisallowed(resolved, order) {
		return TierSelection{}, ErrNoAllowedLanes
	}
	return TierSelection{}, ErrFleetExhausted
}
