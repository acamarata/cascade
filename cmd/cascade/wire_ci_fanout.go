//go:build !windows

package main

// Purpose: the "ci-fanout" daemon registration (P1-CI-06, contract:ci-result-
// routing). It calls ci.RegisterCIFanout once on the daemon's one CI
// dispatcher, so each terminal CI result reaches its delegation origin
// through the daemon's one notify.Service (addressed, scoped, or a CI
// attention item; docs/ci/result-routing.md).
//
// Inputs: w.Deps.CI (the "ci" registration, Order 20), w.Deps.Notify (the
// "notify" registration, Order 5), w.Deps.Fleet's jobs store, w.Store, w.Bus.
// Outputs: exactly one OnTerminal callback on the dispatcher.
// Constraints: no new table; the delegation record is read from the existing
// jobs_delegation table, whose own idempotent migration is applied here.
//
// SPORT: cmd.cascade.wireCIFanout/ADDED (P1-CI-06).

import (
	"context"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

const ciFanoutSubsystem = "ci-fanout"

// registerCIFanout is the one fanout registration call the daemon makes; a
// test counts calls through it.
var registerCIFanout = ci.RegisterCIFanout

var _ = registerDaemonWiring(daemonRegistration{Name: ciFanoutSubsystem, Phase: phaseSupervised, Order: 23, Wire: wireCIFanout})

func wireCIFanout(w *daemonWiring) error {
	if w.Deps.CI == nil || w.Deps.Notify == nil || w.Deps.Fleet == nil || w.Deps.Config == nil {
		w.Manifest.Skipped(ciFanoutSubsystem, "no CI runtime, notify runtime, fleet runtime or config")
		return nil
	}
	jobsDB := w.Deps.Fleet.JobsDB()
	if err := jobs.ApplyDelegationSchema(w.Ctx, jobsDB, migrate.SQLiteEmitter{}, w.Clock, "", ""); err != nil {
		return err
	}
	attention, err := newCIAttentionPusher(w.Store, w.Clock, w.Bus, w.Deps.Config.CIPolicy.PrivateRepos)
	if err != nil {
		return err
	}
	origins := jobDelegationOrigin{store: w.Deps.Fleet.Jobs()}
	registerCIFanout(w.Deps.CI.Dispatcher(), w.Deps.CI.CIDB(), origins, w.Deps.Notify.Service().Notifier, attention)
	w.Manifest.Started(ciFanoutSubsystem, "terminal CI results routed to their delegation origin")
	return nil
}

// jobDelegationOrigin resolves a job's newest delegation record from the
// jobs store (jobs.Store.DelegationByJobID). It knows no scope for a job
// without a record, so such a result becomes a CI attention item.
type jobDelegationOrigin struct {
	store *jobs.Store
}

// OriginForJob returns the newest delegation record whose job_id is jobID;
// found is false when there is none.
func (o jobDelegationOrigin) OriginForJob(ctx context.Context, jobID string) (jobs.DelegationRecord, string, bool, error) {
	if jobID == "" || o.store == nil {
		return jobs.DelegationRecord{}, "", false, nil
	}
	rec, found, err := o.store.DelegationByJobID(ctx, jobID)
	if err != nil || !found {
		return jobs.DelegationRecord{}, "", false, err
	}
	return rec, "", true, nil
}
