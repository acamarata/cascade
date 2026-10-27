//go:build !windows

// Purpose: registers the daemon's "perms.grant" and "perms.revoke"
// handlers, the path `cascade plugin perms` takes while the daemon holds
// the plugin store.
// Inputs: the daemon wiring's plugin store.
// Outputs: both verbs bound to plugins.PermsRPCHandler.
// Constraints: the elevation rows (always elevated) and the access class
// (owner only) are declared by their owners, never here.
// SPORT: cmd/cascade daemon registrations (perms.grant, perms.revoke; P1-SEC-08).
package main

import "github.com/acamarata/cascade/internal/plugins"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-perms", Phase: phaseNamespaces, Order: 42,
	Wire: func(w *daemonWiring) error {
		w.Registry.Register(plugins.PermsGrantRPCMethod, plugins.PermsRPCHandler(w.Store, true))
		w.Registry.Register(plugins.PermsRevokeRPCMethod, plugins.PermsRPCHandler(w.Store, false))
		return nil
	},
})
