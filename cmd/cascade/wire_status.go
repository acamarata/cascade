//go:build !windows

// Purpose: registers the status-get daemon wiring: status.get over the daemon's one manifest; builds the connection counter RunOptions shares.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "status-get", Phase: phaseCore, Order: 10,
	Wire: func(w *daemonWiring) error {
		w.Connections = registerStatusHandler(w.Registry, w.Clock, w.Manifest, w.Settings)
		return nil
	},
})
