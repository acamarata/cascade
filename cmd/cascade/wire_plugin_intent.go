//go:build !windows

// Purpose: registers the daemon's "plugin.intent" handler, the path
// `cascade plugin add <intent-ref>` takes while a daemon runs.
// Inputs: this binary's builtin registry, loaded once at wiring time.
// Outputs: "plugin.intent" bound to plugins.IntentRPCHandler.
// Constraints: the access class (owner only) is declared by its owner,
// never here. A manifest the registry rejects is not routable; the
// rejection is logged and the daemon still starts.
// SPORT: cmd/cascade daemon registrations (plugin.intent; P1-PLG-07).
package main

import "github.com/acamarata/cascade/internal/plugins"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-intent", Phase: phaseNamespaces, Order: 43,
	Wire: func(w *daemonWiring) error {
		reg := &plugins.BuiltinRegistry{}
		if err := reg.Load(); err != nil && w.Logger != nil {
			w.Logger.Warn("plugin intent: builtin registry rejected manifests; they are not routable", "err", err)
		}
		w.Registry.Register(plugins.IntentRPCMethod, plugins.IntentRPCHandler(plugins.NewIntentDispatcher(reg)))
		return nil
	},
})
