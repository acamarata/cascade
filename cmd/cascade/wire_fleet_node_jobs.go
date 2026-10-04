//go:build !windows

// Purpose: registers the fleet-node-jobs daemon wiring: fleet.*, node.*, job.* and lease.*.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-node-jobs", Phase: phaseFleet, Order: 1,
	Wire: func(w *daemonWiring) error {
		return wireFleetNodeAndJobHandlers(w.Registry, w.Store, w.Clock, w.Bus, w.Paths, w.Settings)
	},
})
