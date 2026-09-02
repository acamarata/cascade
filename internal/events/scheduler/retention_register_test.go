// Purpose: RegisterRetentionJobs tests — the AC's "DomainPruner + VacuumJob
//   registered on the scheduler at the 168h default; a test asserts both
//   appear in the scheduled-jobs listing with the correct interval."
//   R-14.117-authorized split of scheduler_test.go.
// SPORT: internal.events.scheduler.RegisterRetentionJobs/ADDED (tests)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestRegisterRetentionJobs_WeeklyInterval is the T-4 AC artifact for
// retention wiring: both DomainPruner and VacuumJob are registered on the
// scheduler at exactly the 168h weekly default, and both appear in the
// scheduled-jobs listing.
func TestRegisterRetentionJobs_WeeklyInterval(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	sched := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	ctx := context.Background()

	cfg := storage.RetentionConfig{Clock: clock}
	// db is intentionally nil: this test only proves REGISTRATION and
	// LISTING (the AC's exact wording), never firing — an actual firing
	// needs a real *sql.DB from providers/sqlite, which this ticket's
	// files_scope forbids touching. See RegisterRetentionJobs' own doc
	// comment and this ticket's CONTRACT-DEVIATIONS report note.
	if err := RegisterRetentionJobs(ctx, sched, nil, cfg, clock); err != nil {
		t.Fatalf("RegisterRetentionJobs: %v", err)
	}

	jobs, err := sched.ListScheduledJobs()
	if err != nil {
		t.Fatalf("ListScheduledJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("ListScheduledJobs() = %+v, want exactly 2 (prune + vacuum)", jobs)
	}

	byOwner := map[string]CronJob{}
	for _, j := range jobs {
		byOwner[j.Owner] = j
	}
	prune, ok := byOwner[retentionPruneOwner]
	if !ok {
		t.Fatalf("no retention-prune job in listing: %+v", jobs)
	}
	vacuum, ok := byOwner[retentionVacuumOwner]
	if !ok {
		t.Fatalf("no retention-vacuum job in listing: %+v", jobs)
	}

	assertWeeklyInterval(t, "prune", prune)
	assertWeeklyInterval(t, "vacuum", vacuum)
	assertBothScheduled(ctx, t, sched)
}

// assertWeeklyInterval asserts job's Spec is an "@every 168h" interval.
func assertWeeklyInterval(t *testing.T, name string, job CronJob) {
	t.Helper()
	d, ok := job.Interval()
	if !ok {
		t.Errorf("%s job Spec %q is not an @every interval", name, job.Spec)
		return
	}
	if d != retentionInterval {
		t.Errorf("%s job interval = %v, want %v (168h weekly default)", name, d, retentionInterval)
	}
}

// assertBothScheduled Activates sched and asserts both retention jobs land
// in Scheduled, not Orphaned (their owners are registered by
// RegisterRetentionJobs).
func assertBothScheduled(ctx context.Context, t *testing.T, sched *Scheduler) {
	t.Helper()
	report, err := sched.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(report.Orphaned) != 0 {
		t.Fatalf("Activate report.Orphaned = %+v, want none", report.Orphaned)
	}
	if len(report.Scheduled) != 2 {
		t.Fatalf("Activate report.Scheduled = %+v, want exactly 2", report.Scheduled)
	}
}

// TestRegisterRetentionJobs_Idempotent proves calling RegisterRetentionJobs
// twice (e.g. every daemon startup re-declaring its built-in jobs) never
// duplicates the two jobs.
func TestRegisterRetentionJobs_Idempotent(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	sched := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	ctx := context.Background()
	cfg := storage.RetentionConfig{Clock: clock}

	if err := RegisterRetentionJobs(ctx, sched, nil, cfg, clock); err != nil {
		t.Fatalf("RegisterRetentionJobs (1st): %v", err)
	}
	if err := RegisterRetentionJobs(ctx, sched, nil, cfg, clock); err != nil {
		t.Fatalf("RegisterRetentionJobs (2nd): %v", err)
	}
	jobs, err := sched.ListScheduledJobs()
	if err != nil {
		t.Fatalf("ListScheduledJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("ListScheduledJobs() after double-registration = %+v, want still exactly 2", jobs)
	}
}
