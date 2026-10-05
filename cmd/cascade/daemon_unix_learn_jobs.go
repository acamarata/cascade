//go:build !windows

// Purpose: apply the learn schema and register reconciliation and retention.
// Inputs: the daemon's scheduler, shared database, paths, clock and event bus.
// Outputs: two persisted jobs and their production runnables.
// Constraints: called before Activate; one scheduler and an injected clock.
// SPORT: cmd/cascade/daemon learn job wiring.
package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// The CronJob owner labels the two learn jobs register under.
const (
	learnOutcomeReconcileOwner = "learn-outcome-reconcile"
	learnRetentionOwner        = "learn-retention"
)

// learnOutcomeReconcileSpec / learnRetentionSpec are the contract-fixed
// schedules (R-14.316): reconcile runs every minute; retention runs daily.
const (
	learnOutcomeReconcileSpec = "@every 1m0s"
	learnRetentionSpec        = "@every 24h0m0s"
)

// learnEventNamespace is the bus namespace RetentionSweep's completion
// event publishes under.
const learnEventNamespace = "learn"

// EventKindLearnRetentionSwept is retention.go's per-run completion event.
const EventKindLearnRetentionSwept events.EventKind = "learn.retention.swept"

// learnRetentionSweptPayload is EventKindLearnRetentionSwept's JSON body.
type learnRetentionSweptPayload struct {
	RowsDeleted int64 `json:"rows_deleted"`
}

// registerLearnJobs applies internal/learn's migration set against rawDB
// and registers both learn runnables on sched. Call BEFORE sched.Activate.
func registerLearnJobs(
	ctx context.Context, sched *scheduler.Scheduler, rawDB *sql.DB,
	paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus,
) error {
	if err := learn.ApplyLearnSchema(ctx, rawDB, migrate.SQLiteEmitter{}, clock); err != nil {
		return err
	}
	// Idempotent application ensures jobs_job exists regardless of startup order.
	if err := jobs.ApplyJobsSchema(ctx, rawDB, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return err
	}
	// The outcome writer joins and updates jobs_usage, even before usage is recorded.
	if err := conductor.ApplyUsageMigrationSchema(ctx, rawDB, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return err
	}
	rec := learn.OutcomeReconciler{
		Store:  jobs.NewStore(rawDB),
		Writer: newLearnOutcomeWriter(rawDB, clock),
	}
	if err := sched.RegisterRunnable(learnOutcomeReconcileOwner, func(runCtx context.Context) error {
		return rec.Reconcile(runCtx)
	}); err != nil {
		return err
	}
	if err := sched.ScheduleJob(ctx, learnOutcomeReconcileOwner, learnOutcomeReconcileSpec, learnOutcomeReconcileOwner); err != nil {
		return err
	}
	return registerLearnRetentionJob(ctx, sched, rawDB, paths, clock, bus)
}

// newLearnOutcomeWriter composes the production outcome and observation writers.
// Inputs: the shared database and clock. Output: the reconciler's writer.
// Constraints: replaced only by serial composition tests; no alternate store.
// SPORT: cmd/cascade/daemon learn outcome composition.
var newLearnOutcomeWriter = func(db *sql.DB, clock runtime.Clock) *learn.SQLiteOutcomeWriter {
	return learn.NewSQLiteOutcomeWriter(db, clock).
		WithObservationWriter(learn.NewSQLiteCapabilityScorer(db, clock))
}

// registerLearnRetentionJob is registerLearnJobs' retention half, split
// out for the 50-line function cap.
func registerLearnRetentionJob(
	ctx context.Context, sched *scheduler.Scheduler, rawDB *sql.DB,
	paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus,
) error {
	sweep := learn.RetentionSweep{DB: rawDB, Clock: clock, Paths: paths}
	if err := sched.RegisterRunnable(learnRetentionOwner, func(runCtx context.Context) error {
		rowsDeleted, err := sweep.Run(runCtx)
		if err != nil {
			return err
		}
		return publishLearnRetentionSwept(runCtx, bus, rowsDeleted)
	}); err != nil {
		return err
	}
	return sched.ScheduleJob(ctx, learnRetentionOwner, learnRetentionSpec, learnRetentionOwner)
}

// publishLearnRetentionSwept publishes {rows_deleted} on the real bus. A
// nil bus (no-bus configuration) is a documented no-op, matching every
// other job sink in this package (memoryJobEventSink.publish).
func publishLearnRetentionSwept(ctx context.Context, bus *events.Bus, rowsDeleted int64) error {
	if bus == nil {
		return nil
	}
	body, err := json.Marshal(learnRetentionSweptPayload{RowsDeleted: rowsDeleted})
	if err != nil {
		return err
	}
	_, err = bus.Publish(ctx, learnEventNamespace, EventKindLearnRetentionSwept, "internal/learn", body)
	return err
}
