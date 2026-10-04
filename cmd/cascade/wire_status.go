//go:build !windows

// Purpose: registers the status-get daemon wiring: status.get; builds the manifest and connection counter RunOptions shares.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "status-get", Phase: phaseCore, Order: 10,
	Wire: func(w *daemonWiring) error {
		w.Manifest, w.Connections = registerStatusHandler(w.Registry, w.Clock, w.Logger, w.Settings)
		return nil
	},
})
