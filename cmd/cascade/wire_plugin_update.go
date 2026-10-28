//go:build !windows

// Purpose: registers the daemon's "plugin.update" handler, the attested
// commit path for an update `cascade plugin update` reports as needing
// elevation.
// Inputs: the daemon wiring's plugin store.
// Outputs: "plugin.update" bound to plugins.UpdateRPCHandler.
// Constraints: the elevation row (always elevated) and the access class
// (owner only) are declared by their owners, never here. The process
// handshaker is plugins.NewProcessHandshaker (the trusted launch path).
// SPORT: cmd/cascade daemon registrations (plugin.update).
package main

import "github.com/acamarata/cascade/internal/plugins"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-update", Phase: phaseNamespaces, Order: 40,
	Wire: func(w *daemonWiring) error {
		w.Registry.Register(plugins.UpdateRPCMethod, plugins.UpdateRPCHandler(w.Store, plugins.NewProcessHandshaker))
		return nil
	},
})
