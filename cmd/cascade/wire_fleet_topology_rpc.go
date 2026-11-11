//go:build !windows

// Purpose: registers the fleet topology RPC adapter and event topic, and
// publishes committed lane health changes from the runtime's one store.
// Inputs: the composed fleet runtime, process detector, bus and clock.
// Outputs: owner-only topology methods and the fleet.topology SSE stream.
// Constraints: no second store; observer errors refuse daemon composition.
// SPORT: cmd/cascade daemon registrations (P1-TOP-11).
package main

import (
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-topology-rpc", Phase: phaseFleet, Order: 70,
	Wire: wireFleetTopologyRPC,
})

var _ = registerDaemonWiring(daemonRegistration{
	// Order 20 belongs to fleet-capacity-topic; slots must be unique.
	Name: "fleet-topology-topic", Phase: phaseEventTopics, Order: 21,
	Wire: wireFleetTopologyTopic,
})

// wireFleetTopologyRPC uses the live runtime store and its ceiling policy.
// A registry built without a fleet still registers methods, whose adapter
// refuses reads as unavailable.
func wireFleetTopologyRPC(w *daemonWiring) error {
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return err
	}
	var store fleetTopologyStore
	var ceiling fleetTopologyCeiling
	if fleet := w.Deps.Fleet; fleet != nil {
		store = fleetTopologyStore{Store: fleet.Topology(), Quota: fleet.Quota()}
		ceiling = store.CeilingForLane
	}
	return rpc.RegisterFleetTopology(w.Registry, fleetTopologyDeps(store, ceiling, detector))
}

// wireFleetTopologyTopic binds the post-commit observer once, to the same
// bus the SSE handler consumes. The bus stamps events with its injected clock.
func wireFleetTopologyTopic(w *daemonWiring) error {
	if fleet := w.Deps.Fleet; fleet != nil {
		emitter := rpc.NewFleetTopologyEmitter(w.Bus)
		if err := fleet.Topology().SetHealthObserver(func(lane topology.LaneID, from, to topology.LaneHealth) {
			emitter.EmitLaneHealthChanged(w.Ctx, rpc.LaneHealthChangedPayload{
				LaneID: string(lane), From: string(from), To: string(to),
				Reason: "stored lane health: " + string(to),
			})
		}); err != nil {
			return err
		}
	}
	w.Events.WithTopic(rpc.FleetTopologyTopic, rpc.NewFleetTopologySSEHandler(w.Bus, w.Clock))
	return nil
}
