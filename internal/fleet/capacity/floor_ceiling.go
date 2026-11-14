// Purpose: the lane-ceiling floor: a LaneFilter that admits a lease only
// when the request's joined tier (JoinSensitivity of the sensitivity and the
// data class) fits the lane's data-class ceiling (topology.TierAdmits).
// Inputs: a cap that reads one lane's Ceiling.
// Outputs: an admit or a refusal with a reason.
// Constraints: a nil cap or a cap error never reads as admit.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// CeilingFloorName is the lane-ceiling floor's Name.
const CeilingFloorName = "lane-ceiling"

// CeilingCap reads one lane's data-class ceiling.
type CeilingCap func(ctx context.Context, id conductor.LaneID, lt conductor.LaneType) (topology.Ceiling, error)

type ceilingFloor struct {
	cap CeilingCap
}

// NewCeilingFloor returns the lane-ceiling floor over ceilingOf. A nil
// ceilingOf admits nothing; its error refuses the lane with the error as reason.
func NewCeilingFloor(ceilingOf func(ctx context.Context, id conductor.LaneID, lt conductor.LaneType) (topology.Ceiling, error)) LaneFilter {
	return ceilingFloor{cap: ceilingOf}
}

// RegistryLaneCeilings is the production cap: the ceiling of the registry
// lane named id in s under its lane type and restricted permit. A nil store
// returns a nil cap (the floor then admits nothing).
func RegistryLaneCeilings(s *topology.Store, permits conductor.RestrictedPermits) CeilingCap {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, id conductor.LaneID, lt conductor.LaneType) (topology.Ceiling, error) {
		return s.CeilingForRegistryLane(ctx, string(id), laneKindOf(lt), permits.Permits(string(id)))
	}
}

// laneKindOf maps the conductor lane type onto the topology lane kind; an
// undeclared type reads as unresolved (the strict one).
func laneKindOf(lt conductor.LaneType) topology.LaneKind {
	switch lt {
	case conductor.LaneControllerLocal:
		return topology.LaneKindControllerLocal
	case conductor.LaneExternalAPI:
		return topology.LaneKindExternalAPI
	case conductor.LaneUnresolved:
		return topology.LaneKindUnresolved
	default:
		return topology.LaneKindUnresolved
	}
}

// Name names the floor in refusal reasons.
func (ceilingFloor) Name() string { return CeilingFloorName }

// Admit reports whether req's joined tier fits slot's ceiling.
func (f ceilingFloor) Admit(req ResourceRequest, slot ProviderSlot) (bool, string) {
	if f.cap == nil {
		return false, "lane-ceiling: no cap"
	}
	c, err := f.cap(context.Background(), slot.LaneID, slot.LaneType)
	if err != nil {
		return false, err.Error()
	}
	tier := provider.JoinSensitivity(req.Sensitivity, provider.TierOrLocalOnly(req.DataClass))
	if !topology.TierAdmits(tier, c) {
		return false, "request tier " + tier.String() + " above lane ceiling " + c.Tier.String() + " (" + string(c.Reason) + ")"
	}
	return true, ""
}
