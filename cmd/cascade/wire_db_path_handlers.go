//go:build !windows

// Purpose: registers the db-path-handlers daemon wiring: recall.index.* and plugin.*, each over its own second sqlite connection.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import "path/filepath"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "db-path-handlers", Phase: phaseLate, Order: 20,
	Wire: func(w *daemonWiring) error {
		dbPath := filepath.Join(w.Paths.DataDir(), "cascade.db")
		return registerDBPathHandlers(w.Ctx, w.Registry, w.Manifest, w.Paths, w.Clock, w.Bus, w.Store, dbPath, bridgeHTTPClientFrom(w.Opts))
	},
})
