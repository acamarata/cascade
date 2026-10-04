//go:build !windows

// Purpose: registers the conductor-expand daemon wiring: conductor.expand.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "conductor-expand", Phase: phaseLate, Order: 10,
	Wire: func(w *daemonWiring) error {
		return wireConductorExpand(w.Ctx, w.Registry, w.Paths, w.Clock)
	},
})
