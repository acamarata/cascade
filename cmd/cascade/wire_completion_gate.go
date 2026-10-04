//go:build !windows

// Purpose: registers the completion-gate daemon wiring: the completion-gate hook pack (see hooks.go's header).
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "completion-gate", Phase: phaseFleet, Order: 5,
	Wire: func(w *daemonWiring) error {
		return wireCompletionHookPack(w.Ctx, w.Registry, w.Store, w.Clock, w.Bus, w.Paths)
	},
})
