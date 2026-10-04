//go:build !windows

// Purpose: registers the fleet-sessions daemon wiring: fleet.sessions.list.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import "github.com/acamarata/cascade/internal/daemon"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-sessions", Phase: phaseFleet, Order: 3,
	Wire: func(w *daemonWiring) error {
		daemon.RegisterFleetSessionsHandler(w.Registry, w.Store, w.Clock, w.Bus)
		return nil
	},
})
