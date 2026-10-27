//go:build !windows

// Purpose: mounts the plugin-install middleware that publishes
// plugin.registered after every successful plugin.add or plugin.update.
// Inputs: the daemon wiring's event bus, plugin store and logger.
// Outputs: w.Registry.Use(plugins.InstallMiddleware(...)).
// Constraints: phaseMiddleware order 30, inside the principal (5) and
// elevation (10) middleware, so it sees the attested `_args` only.
// SPORT: cmd/cascade daemon registrations (plugin-install).
package main

import "github.com/acamarata/cascade/internal/plugins"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-install", Phase: phaseMiddleware, Order: 30,
	Wire: func(w *daemonWiring) error {
		w.Registry.Use(plugins.InstallMiddleware(w.Bus, w.Store, w.Logger))
		return nil
	},
})
