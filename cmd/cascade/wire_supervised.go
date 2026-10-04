//go:build !windows

// Purpose: registers the supervised daemon wiring: the DAG scheduler and the session watch.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "supervised", Phase: phaseSupervised, Order: 10,
	Wire: func(w *daemonWiring) error {
		return wireSupervisedSubsystems(w.Ctx, w.Manifest, w.Bus, w.Clock, w.Paths, w.Store, w.Settings)
	},
})
