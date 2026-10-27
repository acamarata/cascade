//go:build !windows

// Purpose: mounts the operator-presence middleware: it feeds the daemon's one
// PresenceTracker, which away mode reads as its PresenceSource.
// Inputs: the daemon wiring's registry and clock.
// Outputs: w.Deps.Presence (read by later registrations) and
// w.Registry.Use(daemon.PresenceMiddleware(...)).
// Constraints: phaseMiddleware order 20, registered after the principal (5)
// and elevation (10) middleware and so INSIDE them: the elevation middleware
// has already attached its verified witness to the context presence receives.
// Registering it before elevation would see no witness and count nothing.
// Exactly one tracker is built per composition; a wiring that already carries
// one keeps it. The tracker is bound to the elevation middleware's verifier
// (w.Deps.ElevationVerifier), so only witnesses that middleware minted count;
// a wiring without one counts nothing.
// SPORT: cmd/cascade daemon registrations (P1-HRN-10).
package main

import "github.com/acamarata/cascade/internal/daemon"

var _ = registerDaemonWiring(daemonRegistration{Name: "presence", Phase: phaseMiddleware, Order: 20, Wire: wirePresence})

// newPresenceTracker is the one constructor call the presence registration
// makes; a test captures the tracker through it.
var newPresenceTracker = daemon.NewPresenceTracker

// wirePresence builds the tracker (unless the wiring carries one) and mounts
// its middleware.
func wirePresence(w *daemonWiring) error {
	if w.Deps.Presence == nil {
		w.Deps.Presence = newPresenceTracker(w.Clock)
	}
	w.Deps.Presence.Bind(w.Deps.ElevationVerifier)
	w.Registry.Use(daemon.PresenceMiddleware(w.Deps.Presence))
	return nil
}
