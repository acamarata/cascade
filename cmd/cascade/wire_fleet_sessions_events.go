//go:build !windows

// Purpose: registers the fleet-sessions-events daemon wiring: the fleet.sessions topic on GET /events.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import "github.com/acamarata/cascade/internal/fleet/sessions"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "fleet-sessions-events", Phase: phaseEventTopics, Order: 10,
	Wire: func(w *daemonWiring) error {
		w.Events.WithTopic(sessions.Topic, sessions.NewSSEHandler(w.Bus, w.Clock))
		return nil
	},
})
