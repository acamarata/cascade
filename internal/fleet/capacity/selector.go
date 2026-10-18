// Purpose: contract:lane-selector-seam, the one place Fleet.Lease asks "which
// tier and which lane". The interim implementation (tier_selector.go) wraps
// TierPolicy.SelectTier; a later selector built on economics.Rank replaces it
// behind the same interface.
//
// Inputs: the snapshot, the lanes that survived the sensitivity filter and
// every floor (sorted, never nil), the normalized request, the scorer and the
// attempt number.
// Outputs: the tier selection, the lane of that tier and its score.
// Constraints: a Selector never widens Eligible; it only chooses from it.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"slices"

	"github.com/acamarata/cascade/internal/conductor"
)

// SelectInput is everything a Selector may read for one request.
type SelectInput struct {
	Snapshot FleetSnapshot
	// Eligible is sorted, already filtered and never nil.
	Eligible []conductor.LaneID
	Request  ResourceRequest
	Scorer   CapabilityScorer
	Attempt  int
}

// Selection is a Selector's answer: the tier decision, the lane that serves
// it and the capability score read at selection.
type Selection struct {
	Tier  TierSelection
	Lane  conductor.LaneID
	Score float64
}

// Selector chooses a tier and a lane from SelectInput.Eligible.
type Selector interface {
	Select(ctx context.Context, in SelectInput) (Selection, error)
}

// isolate returns a copy of snap whose lane, provider and node maps are the
// caller's own. Clone does not carry Lanes or Quota, so the lanes are copied
// here; Quota is shared read-only.
func isolate(snap FleetSnapshot) FleetSnapshot {
	out := Clone(snap)
	out.Quota = snap.Quota
	if snap.Lanes != nil {
		out.Lanes = make(map[conductor.LaneID]ProviderSlot, len(snap.Lanes))
		for id, slot := range snap.Lanes {
			slot = cloneProviderSlot(slot)
			slot.Posture.AuthModes = slices.Clone(slot.Posture.AuthModes)
			slot.Posture.AutomationModes = slices.Clone(slot.Posture.AutomationModes)
			out.Lanes[id] = slot
		}
	}
	return out
}

// laneFacts is what Lease read about an eligible lane before the Selector ran.
type laneFacts struct{ tier Tier }

// captureFacts records the eligible lanes and their tiers in a map the
// Selector cannot reach: it is the independent membership check on the answer.
func captureFacts(snap FleetSnapshot, eligible []conductor.LaneID) map[conductor.LaneID]laneFacts {
	facts := make(map[conductor.LaneID]laneFacts, len(eligible))
	for _, id := range eligible {
		facts[id] = laneFacts{tier: snap.Lanes[id].Tier}
	}
	return facts
}

// verifySelection refuses a Selection whose lane was not eligible before the
// Selector ran, or whose reported tier is not the lane's own tier.
func verifySelection(facts map[conductor.LaneID]laneFacts, sel Selection) (Selection, error) {
	fact, ok := facts[sel.Lane]
	switch {
	case !ok:
		return Selection{}, refuse(ErrNoCapableTier, nil, "selector chose lane %q outside the eligible set", sel.Lane)
	case fact.tier != sel.Tier.Tier:
		return Selection{}, refuse(ErrNoCapableTier, nil, "selector reported tier %q for lane %q, which is tier %q", sel.Tier.Tier, sel.Lane, fact.tier)
	}
	return sel, nil
}
