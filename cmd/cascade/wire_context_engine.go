//go:build !windows

// Purpose: registers the context-engine daemon wiring: context.*; runs before conductor because ApplyGraphSchema needs the scope schema.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "context-engine", Phase: phaseNamespaces, Order: 20,
	Wire: func(w *daemonWiring) error {
		return registerContextEngineHandlers(w.Registry, w.Paths, w.Clock, w.Bus)
	},
})
