//go:build !windows

// Purpose: registers bundle.install, bundle.activate, bundle.status and
// bundle.rollback (namespace harness.bundle) on the daemon registry over one
// bundle service.
// Inputs: w.Paths (record directory <data>/bundles), w.Clock, and the
// process's one redacting audit log (openAuditLog over w.Store).
// Outputs: four registered methods.
// Constraints: the elevation row and access classes are declared in the verb
// files, never here. A wiring whose audit log cannot be opened still
// registers all four verbs; rollback then refuses with KindUnavailable, so it
// never answers without its audit event. No goroutine is started.
// SPORT: cmd/cascade bundle verbs wiring (P1-HRN-16).
package main

import "path/filepath"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "bundle-install", Phase: phaseNamespaces, Order: 50,
	Wire: func(w *daemonWiring) error {
		mountBundleVerbs(w.Registry, productionBundleService(w))
		return nil
	},
})

// productionBundleService builds the bundle service for w.
func productionBundleService(w *daemonWiring) *bundleService {
	dir := ""
	if w.Paths != nil {
		dir = filepath.Join(w.Paths.DataDir(), "bundles")
	}
	var auditor bundleAuditor
	if w.Store != nil && w.Paths != nil {
		if log, err := openAuditLog(w.Store, w.Clock, w.Bus, w.Paths); err == nil && log != nil {
			auditor = log
		}
	}
	return newBundleService(dir, w.Clock, auditor)
}
