//go:build !windows

// Purpose: registers the fleet-capacity daemon wiring: fleet.capacity over the
// fleet runtime's one compositor, and the fleet.capacity topic on GET /events.
// Both are owner-only (declared beside the handler in internal/fleet/capacity/rpc.go:
// cmd/cascade may not import internal/rpc). No fleet.lease RPC: the lease stays in process.
// SPORT: cmd/cascade daemon registrations (P1-CAP-05).
package main

import (
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/capacity"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-capacity", Phase: phaseFleet, Order: 10,
	Wire: func(w *daemonWiring) error {
		capacity.RegisterHandlers(w.Registry, fleetCompositor(w.Deps.Fleet))
		return nil
	},
})

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-capacity-topic", Phase: phaseEventTopics, Order: 20,
	Wire: func(w *daemonWiring) error {
		w.Events.WithTopic(capacity.FleetCapacityTopic, capacity.NewSSEHandler(w.Bus, w.Clock))
		return nil
	},
})

// fleetCompositor is the runtime's compositor, or nil for a wiring built
// without one; fleet.capacity then refuses KindUnavailable.
func fleetCompositor(fleet *daemon.FleetRuntime) *capacity.Compositor {
	if fleet == nil {
		return nil
	}
	return fleet.Compositor()
}
