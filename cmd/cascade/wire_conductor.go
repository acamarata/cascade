//go:build !windows

// Purpose: registers the conductor daemon wiring: conductor.execute and jobs.reachability.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "conductor", Phase: phaseConductor, Order: 10,
	Wire: func(w *daemonWiring) error {
		fanOut, err := wireConductorAndReachability(w.Ctx, w.Registry, w.Manifest, w.Paths, w.Clock, w.Store, nodeTunnelLookup(w.Opts))
		w.ConductorFanOut = fanOut
		return err
	},
})
