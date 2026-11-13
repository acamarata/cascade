//go:build !windows

// Purpose: mounts fleet.health.summary on the daemon's one RPC registry,
// reading the composed fleet runtime's health.
// Inputs: w.Deps.Fleet (the one daemon.FleetRuntime) and w.Registry.
// Outputs: the owner-view fleet.health.summary method, or a manifest skip
// when the daemon has no fleet runtime.
// Constraints: the five keys are copied from FleetRuntime.Health through
// executive.SummarizeFleetHealth, never recomputed here; an unknown
// percentage stays nil so the response omits it.
// SPORT: cmd/cascade daemon registrations (fleet health summary).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/executive"
	"github.com/acamarata/cascade/internal/rpc"
)

// Order 7 is the next free phaseFleet slot after jobs-evidence (6).
var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-health", Phase: phaseFleet, Order: 7,
	Wire: wireFleetHealth,
})

// wireFleetHealth registers fleet.health.summary over the composed fleet
// runtime; a daemon without one records the skip and serves no method.
func wireFleetHealth(w *daemonWiring) error {
	fleet := w.Deps.Fleet
	if fleet == nil {
		w.Manifest.Skipped("fleet-health", "no fleet runtime")
		return nil
	}
	return rpc.RegisterFleetHealthSummary(w.Registry, rpc.FleetHealthDeps{Summary: fleetHealthSummary(fleet)})
}

// fleetHealthSummary adapts FleetRuntime.Health to the rpc summary seam. It
// calls Health once per request and copies key, state and percentage.
func fleetHealthSummary(fleet *daemon.FleetRuntime) func(context.Context) (rpc.FleetHealthSummaryDTO, error) {
	return func(ctx context.Context) (rpc.FleetHealthSummaryDTO, error) {
		summary, err := executive.SummarizeFleetHealth(ctx, fleet.Health)
		if err != nil {
			return rpc.FleetHealthSummaryDTO{}, err
		}
		var out rpc.FleetHealthSummaryDTO
		for i, k := range summary.Keys {
			out.Keys[i] = rpc.FleetHealthKeyDTO{Key: string(k.Key), State: string(k.State), Percentage: k.Percentage}
		}
		return out, nil
	}
}
