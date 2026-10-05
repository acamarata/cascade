// Purpose: project eligible snapshot lanes into tier policy inputs.
// Inputs: a snapshot and admitted lane IDs. Outputs: per-tier observations.
// Constraints: deterministic ordering, no I/O or clock reads.
// SPORT: fleet.capacity.policy.

package capacity

import (
	"slices"

	"github.com/acamarata/cascade/internal/conductor"
)

// ProjectTiers projects eligible lanes into tier policy observations. IDs are
// sorted and deduplicated; ties select the first profile in lane order. Empty
// tiers are absent, and lanes with no mapped tier never enter the projection.
func ProjectTiers(snap FleetSnapshot, eligible []conductor.LaneID) map[Tier]TierSlot {
	ids := slices.Clone(eligible)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make(map[Tier]TierSlot)
	for _, id := range ids {
		slot, ok := snap.Lanes[id]
		if !ok || slot.Tier == "" {
			continue
		}
		tier, exists := out[slot.Tier]
		if !exists || projectionStateRank(slot.State) < projectionStateRank(tier.State) {
			tier.State = slot.State
			tier.ProfileID = slot.ProfileRef
		}
		tier.Providers = append(tier.Providers, id)
		tier.Source = SourceProviderStatus
		if !exists || slot.UpdatedAt.After(tier.ObservedAt) {
			tier.ObservedAt = slot.UpdatedAt
		}
		out[slot.Tier] = tier
	}
	return out
}

// projectionStateRank orders usable states first; every other state ties.
func projectionStateRank(state State) int {
	switch state {
	case StateAvailable:
		return 0
	case StateConstrained:
		return 1
	default:
		return 2
	}
}
