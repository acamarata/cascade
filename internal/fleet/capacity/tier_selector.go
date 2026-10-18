// Purpose: the interim Selector. It keeps the lanes that can be scheduled now
// (fresh availability, profile not in backoff), projects them into per-tier
// slots (ProjectTiers), asks TierPolicy.SelectTier for the tier (the ONE tier
// walk, jump rule and reserve flag in the tree) and takes the first of those
// lanes in the chosen tier. A later selector built on economics.Rank replaces
// it and deletes this file.
//
// Inputs: a SelectInput.
// Outputs: a Selection, or SelectTier's ErrFleetExhausted / ErrNoAllowedLanes,
// or ErrTier0Reserved when the reserve flag alone kept tier-0 out.
// Constraints: no walk, jump or prior is re-implemented here. A lane whose
// availability is unknown, or available but stale (the staleness rule degrades
// only available), is never leased and never probed: a probe admission needs a
// dispatcher that classifies the outcome, and this seam has none, so the
// admission would stay in flight with no work behind it.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"slices"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
)

// tierPolicySelector adapts a TierPolicy to the Selector seam.
type tierPolicySelector struct{ policy *TierPolicy }

// NewTierPolicySelector returns the interim Selector over p.
func NewTierPolicySelector(p *TierPolicy) Selector { return tierPolicySelector{policy: p} }

// Select implements Selector.
func (s tierPolicySelector) Select(_ context.Context, in SelectInput) (Selection, error) {
	v := newWalkView(s.policy, in.Scorer)
	usable := usableLanes(in, v)
	slots := ProjectTiers(in.Snapshot, usable)
	sel, err := v.policy.SelectTier(slots, usable, in.Request, v.scorer, in.Attempt)
	v.freeze()
	if err != nil {
		if hasSentinel(err, ErrFleetExhausted) && tier0Reserved(v, in, usable, slots) {
			return Selection{}, refuse(ErrTier0Reserved, err, "tier-0 is the only schedulable tier and reserve_tier0 is set")
		}
		return Selection{}, err
	}
	lane, ok := firstLaneOfTier(in.Snapshot, usable, sel.Tier)
	if !ok {
		return Selection{}, ErrNoCapableTier
	}
	return Selection{Tier: sel, Lane: lane, Score: scoreSafe(v.scorer, sel.Tier, in.Request.TaskClass)}, nil
}

// usableLanes are the eligible lanes (sorted, deduplicated, never nil) that
// can be scheduled now on their own observation: an available state not stale,
// or a constrained state, and a profile outside its backoff. Judging each lane
// before the tier projection keeps one backed-off profile from standing for
// its healthy siblings. The backoff question is the read-only TryAdmit(profile,
// false); no probe is ever admitted.
func usableLanes(in SelectInput, v *walkView) []conductor.LaneID {
	ids := slices.Clone(in.Eligible)
	slices.Sort(ids)
	out := make([]conductor.LaneID, 0, len(ids))
	for _, id := range slices.Compact(ids) {
		slot, ok := in.Snapshot.Lanes[id]
		if !ok || slot.Tier == "" {
			continue
		}
		st := DegradeStaleness(slot.State, SourceProviderStatus, slot.UpdatedAt, v.now)
		if schedulable(st) && v.probes.TryAdmit(slot.ProfileRef, false, v.now) {
			out = append(out, id)
		}
	}
	return out
}

// tier0Reserved reports whether an exhausted walk was refused only because the
// request reserves tier-0: tier-0 is schedulable and no jump rule admitted it.
// It asks the policy's own resolve and jump functions; it does not walk the
// tiers again.
func tier0Reserved(v *walkView, in SelectInput, usable []conductor.LaneID, slots map[Tier]TierSlot) bool {
	if !in.Request.ReserveTier0 {
		return false
	}
	p := v.policy
	resolved := make(map[Tier]resolvedSlot, len(walkTiers))
	for _, t := range walkTiers {
		resolved[t] = p.resolveSlot(t, slots[t], usable, v.now)
	}
	if !schedulable(resolved[TierZero].state) {
		return false
	}
	tc := in.Request.TaskClass
	_, bestLower, haveLower := p.bestLower(resolved, tc, v.scorer)
	jump := evaluateJump(in.Request, in.Attempt, scoreSafe(v.scorer, TierZero, tc), p.estimateFor(TierZero, tc, v.scorer), bestLower, haveLower)
	return !jump.fire
}

// firstLaneOfTier returns the first usable lane (LaneID order) backing tier.
func firstLaneOfTier(snap FleetSnapshot, usable []conductor.LaneID, tier Tier) (conductor.LaneID, bool) {
	for _, id := range usable {
		if slot, ok := snap.Lanes[id]; ok && slot.Tier == tier {
			return id, true
		}
	}
	return "", false
}

// The frozen view of one Select call. The tier walk and the reserve-cause check
// that follows an exhausted walk must read the same clock, scores, estimates and
// probe answers, so the cause names the decision the walk actually made.

// walkView is the frozen inputs of one Select call.
type walkView struct {
	policy *TierPolicy
	scorer CapabilityScorer
	probes *replayProbes
	now    time.Time
}

func newWalkView(p *TierPolicy, scorer CapabilityScorer) *walkView {
	now := p.Clock.Now()
	v := &walkView{
		scorer: &memoScorer{inner: scorer, seen: map[scoreKey]float64{}},
		probes: &replayProbes{inner: p.Probes, seen: map[probeKey]bool{}},
		now:    now,
	}
	v.policy = &TierPolicy{Estimator: &memoEstimator{inner: p.Estimator, seen: map[scoreKey][2]time.Duration{}}, Probes: v.probes, Clock: frozenClock{now}}
	return v
}

// freeze makes later reads replay the first answers.
func (v *walkView) freeze() { v.probes.replay = true }

// frozenClock answers one instant.
type frozenClock struct{ t time.Time }

func (c frozenClock) Now() time.Time { return c.t }

// scoreKey is a (tier, task class) question.
type scoreKey struct {
	tier Tier
	tc   conductor.TaskClass
}

// memoScorer remembers each score it returned, a recovered panic included.
type memoScorer struct {
	inner CapabilityScorer
	seen  map[scoreKey]float64
}

func (m *memoScorer) Score(tier Tier, tc conductor.TaskClass) float64 {
	k := scoreKey{tier, tc}
	if v, ok := m.seen[k]; ok {
		return v
	}
	v := scoreSafe(m.inner, tier, tc) // the recovered fallback is what gets remembered
	m.seen[k] = v
	return v
}

// memoEstimator remembers each estimate it returned.
type memoEstimator struct {
	inner EstimateSource
	seen  map[scoreKey][2]time.Duration
}

func (m *memoEstimator) Estimate(tier Tier, tc conductor.TaskClass) (time.Duration, time.Duration) {
	k := scoreKey{tier, tc}
	if v, ok := m.seen[k]; ok {
		return v[0], v[1]
	}
	qw, de := m.inner.Estimate(tier, tc)
	m.seen[k] = [2]time.Duration{qw, de}
	return qw, de
}

// probeKey identifies one TryAdmit question.
type probeKey struct {
	profile  string
	dispatch bool
}

// replayProbes forwards each admission question to the real ProbeAdmission once
// and answers every repeat from what it saw; after replay is set, a question
// never asked reads as refused. So the lane prefilter, the tier walk and the
// reserve-cause check of one call share one backoff reading even when another
// goroutine classifies a profile between them, and a second look never admits a
// second probe.
type replayProbes struct {
	inner  ProbeAdmission
	seen   map[probeKey]bool
	replay bool
}

func (r *replayProbes) TryAdmit(profile string, dispatchProbe bool, now time.Time) bool {
	k := probeKey{profile, dispatchProbe}
	if ok, asked := r.seen[k]; asked {
		return ok
	}
	if r.replay {
		return false
	}
	ok := r.inner.TryAdmit(profile, dispatchProbe, now)
	r.seen[k] = ok
	return ok
}

func (r *replayProbes) Classify(profile string, outcome State, now time.Time) {
	r.inner.Classify(profile, outcome, now)
}
