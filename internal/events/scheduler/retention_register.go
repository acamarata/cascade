// Purpose: RegisterRetentionJobs — this ticket's registration-owner
//   responsibility for B/S-03.T2's DomainPruner and VacuumJob
//   (internal/storage/retention.go's package doc: "C/S-04.T4's cron
//   scheduler ... wraps them in func(context.Context) error closures at
//   ITS OWN registration site"). Task 4 assigns this content to
//   scheduler.go alongside the tick loop; it is split into this sibling
//   file under R-14.117's authorized-split allowance (Art.10.3's 300-line
//   cap — scheduler.go is already near the cap from the loop itself) AND
//   because it is the one file in this package that needs
//   internal/storage/database/sql, so a caller that does not want
//   retention wiring never pulls that dependency in transitively.
// Inputs: a *Scheduler (already constructed, not yet Activated), an open
//   *sql.DB, and a storage.RetentionConfig.
// Outputs: two persisted CronJob records ("retention-prune",
//   "retention-vacuum") and their registered Runnables, both at the
//   weekly (168h) default this ticket's contract names explicitly (not an
//   invented default — see 08-INIT-CONFIG-SPEC.md §3, which declares no
//   [scheduler] section at all, so per R-14.107's precedent this package
//   invents no OTHER numeric default anywhere; 168h is the one value the
//   contract itself supplies).
// Constraints: this ticket's files_scope forbids editing
//   internal/storage/** and providers/** — RegisterRetentionJobs only
//   IMPORTS internal/storage (for DomainPruner/VacuumJob/RetentionConfig)
//   and database/sql (for the *sql.DB type), neither of which is an edit
//   to those packages.
// SPORT: internal.events.scheduler.RegisterRetentionJobs/ADDED
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
)

// retentionInterval is the weekly cadence this ticket's contract names
// for both registered retention runnables ("register ... as scheduled
// runnables at the weekly (168h) default").
const retentionInterval = 168 * time.Hour

// retentionPruneOwner and retentionVacuumOwner are the CronJob.Owner
// labels RegisterRetentionJobs uses; ListScheduledJobs and OrphanedJobs
// report them under these exact names.
const (
	retentionPruneOwner  = "retention-prune"
	retentionVacuumOwner = "retention-vacuum"
)

// RegisterRetentionJobs registers B/S-03.T2's DomainPruner and VacuumJob
// on s as scheduled runnables at the weekly (168h) default, and persists
// their CronJob definitions (idempotent — safe to call on every daemon
// startup, matching ScheduleJob's own upsert-preserving-LastFire
// behavior). db is the open cascade.db handle both runnables execute
// against; clock supplies VacuumJob's own Clock field (retention.go
// requires it non-nil). Call this BEFORE Activate so the two jobs are
// scheduled rather than reported as orphaned.
//
// Art.1 honesty note: this is the ONLY subsystem this ticket
// pre-registers. The full_desc names memory digest (G/S-14.T3, a weekly
// event) as a future consumer of this same scheduler — it is
// DELIBERATELY NOT registered here, because no runnable for it exists
// yet in this tree; a placeholder registration with no real Runnable
// behind it would be exactly the stub Article 1 forbids. G/S-14.T3 (or
// whichever ticket lands it) calls RegisterRunnable/ScheduleJob itself
// once its Runnable exists.
func RegisterRetentionJobs(ctx context.Context, s *Scheduler, db *sql.DB, cfg storage.RetentionConfig, clock runtime.Clock) error {
	pruner := storage.DomainPruner{}
	cfg = cfg.Normalize()
	if err := s.RegisterRunnable(retentionPruneOwner, func(ctx context.Context) error {
		_, err := pruner.Prune(ctx, db, cfg)
		return err
	}); err != nil {
		return err
	}
	if err := s.ScheduleJob(ctx, retentionPruneOwner, everySpec(retentionInterval), retentionPruneOwner); err != nil {
		return err
	}

	vac := storage.VacuumJob{Clock: clock}
	if err := s.RegisterRunnable(retentionVacuumOwner, func(ctx context.Context) error {
		_, err := vac.Run(ctx, db)
		return err
	}); err != nil {
		return err
	}
	return s.ScheduleJob(ctx, retentionVacuumOwner, everySpec(retentionInterval), retentionVacuumOwner)
}

// everySpec renders d as an "@every <duration>" CronJob.Spec string.
func everySpec(d time.Duration) string {
	return "@every " + d.String()
}
