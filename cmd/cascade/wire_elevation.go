//go:build !windows

// Purpose: mounts the one ElevationMiddleware on the daemon's registry: the
// custody gate, the trust bound to the custody key, one nonce ledger and the
// process's single redacting audit log as the auditor.
// Inputs: the daemon wiring (registry, store, bus, clock, paths).
// Outputs: w.Registry.Use(mw), an "elevation" manifest line saying what it enforces.
// Constraints: first in the middleware chain (phaseMiddleware 100, order 10;
// the access policy sits at order 5 and later middleware uses order > 10), so
// every dispatched verb passes it. A daemon that cannot open the audit log or
// bind a trust record still starts: the middleware then refuses every elevated
// verb and serves the rest. Trust is bound once, here; enrolment needs a restart.
// SPORT: cmd/cascade daemon registrations (P1-SEC-03).
package main

import (
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/elevation"
)

var _ = registerDaemonWiring(daemonRegistration{Name: "elevation", Phase: phaseMiddleware, Order: 10, Wire: wireElevation})

// daemonElevationSelector builds the custody selector the daemon's elevation
// middleware reads. Tests replace it to inject custody sources.
var daemonElevationSelector = func(dataDir string) elevation.Selector {
	// Assigned, not written as a literal: the production source list is
	// elevation.DefaultSources and nothing else (TestNoSelectorLiteralOutsideElevation).
	var sel elevation.Selector
	sel.DataDir, sel.Sources = dataDir, elevation.DefaultSources()
	return sel
}

// wireElevation builds and mounts the middleware. A wiring with no paths (a
// test-built registry) gets the refuse-all variant.
func wireElevation(w *daemonWiring) error {
	if w.Paths == nil {
		w.Registry.Use(daemon.RefuseAllElevation(w.Clock))
		return nil
	}
	dataDir := w.Paths.DataDir()
	log, err := openDaemonAuditLog(w.Store, w.Clock, w.Bus, w.Paths)
	if err != nil {
		log = nil
		w.Manifest.Failed("elevation-audit", err.Error())
	}
	mw, status := daemon.NewElevationMiddleware(dataDir, daemonElevationSelector(dataDir), log, w.Clock)
	w.Registry.Use(mw)
	bindPolicyElevation(w, status) // the policy attestor reads this issuer's witnesses
	detail := "tier " + status.Tier + ", bound"
	if !status.Bound {
		detail = "tier " + status.Tier + ", elevated verbs refused: " + status.Reason
	}
	w.Manifest.Started("elevation", detail)
	return nil
}
