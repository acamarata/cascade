//go:build !windows

// Purpose: registers the owner-only learn.status, learn.explain and
// learn.reset verbs on the composed daemon registry.
// Inputs: the daemon's shared database (the jobs-domain handle the learn
// jobs apply their schema to) and its injected clock.
// Outputs: the three learn verbs served by internal/learn.RegisterLearnHandlers.
// Constraints: one scorer and one estimate source over the same database;
// no second priors table or estimate implementation.
// SPORT: cmd/cascade daemon registrations (learn read and reset verbs).
package main

import "github.com/acamarata/cascade/internal/learn"

var _ = registerDaemonWiring(daemonRegistration{
	// Order 70 belongs to fleet-topology-rpc; slots must be unique.
	Name: "learn-status", Phase: phaseFleet, Order: 71,
	Wire: wireLearnStatus,
})

// wireLearnStatus builds the real scorer, resetter and estimate source over
// the daemon's shared database and registers the learn verbs. A registry
// built without the daemon runtime (no database) leaves the verbs
// unmounted, so a caller sees method-not-found, never a stand-in answer.
func wireLearnStatus(w *daemonWiring) error {
	db := w.Deps.RawDB
	if db == nil {
		return nil
	}
	return learn.RegisterLearnHandlers(w.Registry, learn.LearnReadDeps{
		Scorer:    learn.NewSQLiteCapabilityScorer(db, w.Clock),
		Resetter:  &learn.SQLiteObservationResetter{DB: db},
		Estimates: learn.NewSQLiteEstimateSource(db),
		Clock:     w.Clock,
	})
}
