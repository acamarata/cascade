// Purpose: adapt registered agent lanes into capacity slots.
// Inputs: driver metadata and an injected instant. Outputs: lane slots.
// Constraints: no registry, credentials, or clock access.
// SPORT: fleet.capacity.policy.

package capacity

import (
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// AgentLane is the registered driver's routing and compliance metadata.
type AgentLane struct {
	DriverID string
	Posture  provider.CompliancePosture
	LaneType conductor.LaneType
}

// AgentLaneSlots builds available tier-zero slots from registered drivers.
func AgentLaneSlots(lanes []AgentLane, now time.Time) (map[string]ProviderSlot, error) {
	out := make(map[string]ProviderSlot, len(lanes))
	for _, lane := range lanes {
		if lane.DriverID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "capacity: agent driver ID is required")
		}
		id := "agent:" + lane.DriverID
		out[id] = ProviderSlot{
			LaneID: conductor.LaneID(id), ProfileRef: lane.DriverID,
			Posture: lane.Posture, State: StateAvailable, Tier: TierZero,
			LaneType: lane.LaneType, UpdatedAt: now,
		}
	}
	return out, nil
}
