package daemon

// Purpose: proves RegisterScheduler (subsystems_scheduler.go) actually
//
//	runs Scheduler.Resume against a real *jobs.Store and a real
//	journal.Store -- DEFECT-scheduler-resume-never-called's fix -- rather
//	than constructing a Scheduler nothing ever resumes. Split from
//	subsystems_scheduler_test.go purely to stay under the 300-line cap
//	(mechanical split, same file's existing precedent for
//	scheduler_reenter.go/scheduler_resume_kill9_test.go in internal/jobs).
//
// SPORT: internal/daemon (FIX, DEFECT-scheduler-resume-never-called).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fixedResumeNow is this file's injected "now" (Art.7.1: no bare
// time.Now). IssuedAt=1 plus TTL+grace (7800s) is long past by this
// instant, so seedStaleRunningJob's lease is always stale under it.
var fixedResumeNow = time.Unix(10_000, 0)

// newDaemonTestResumeStore is newDaemonTestJobsStore's (subsystems_test.go)
// sibling: Resume's own reconcileOutbox step reads jobs_outbox, so this
// file's store needs both schemas applied, matching internal/jobs' own
// newResumeTestStore precedent (scheduler_resume_test.go).
func newDaemonTestResumeStore(t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	clock := runtime.NewFixedClock(fixedResumeNow)
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyOutboxSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	return jobs.NewStore(db)
}

// schedulerResumeTestJob returns a minimally-valid `running` jobs.Job,
// mirroring internal/jobs' own baseJob test fixture (unexported there,
// so this package builds its own from the exported Job shape).
func schedulerResumeTestJob(id string) jobs.Job {
	return jobs.Job{
		ID: id, State: jobs.JobStateRunning, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "repo:/tmp/x", RiskClass: "normal",
		MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1.0,
		Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}
}

// seedStaleRunningJob puts a `running` job holding a lease issued long
// enough ago (TTL 2h + grace 10m = 7800s) to be stale, then appends one
// journal entry under its id so reenterStaleJobs' "only re-enter a job the
// real journal recorded progress for" check passes.
func seedStaleRunningJob(t *testing.T, store *jobs.Store, js journal.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if err := store.PutJob(ctx, schedulerResumeTestJob(id)); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutLease(ctx, jobs.ResourceLease{
		RepoID: "repo-1", ScopeGlob: "a/**", Holder: id, IssuedAt: 1,
		TTLSeconds: jobs.DefaultLeaseDefaults().TTLSeconds, Epoch: 1, State: jobs.LeaseHeld,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	if _, err := js.Append(ctx, id, journal.KindIntent, "seed-"+id, []byte(`{}`)); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}
}

// TestRegisterScheduler_ResumesStaleRunningJobAtLeased is the mutation-
// tested proof: with runSchedulerResume's call inside RegisterScheduler
// present, a `running` job seeded with a stale lease before Register is
// observably `leased` IN THE STORE afterward. Commenting out
// runSchedulerResume's call (this ticket's journal records the mutation)
// turns it RED. now is fixed far enough past IssuedAt=1 for TTL+grace
// (7800s) to have elapsed.
func TestRegisterScheduler_ResumesStaleRunningJobAtLeased(t *testing.T) {
	store := newDaemonTestResumeStore(t)
	js := journal.New(storetest.NewMemStore(), runtime.NewFixedClock(fixedResumeNow), journal.DefaultNamespace)
	seedStaleRunningJob(t, store, js, "resume-job-1")

	bus := newSchedulerTestBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManifest(nil, runtime.NewFixedClock(fixedResumeNow))
	sched, err := m.RegisterScheduler(ctx, bus, runtime.NewFixedClock(fixedResumeNow), nil, "sched-test-resume", store, js, 0)
	if err != nil {
		t.Fatalf("RegisterScheduler: %v", err)
	}
	if sched == nil {
		t.Fatal("RegisterScheduler returned a nil Scheduler")
	}

	got, ok, err := store.GetJob(context.Background(), "resume-job-1")
	if err != nil || !ok {
		t.Fatalf("GetJob(resume-job-1) after RegisterScheduler: ok=%v err=%v", ok, err)
	}
	if got.State != jobs.JobStateLeased {
		t.Fatalf("job state in store = %q, want %q (event alone is not application)", got.State, jobs.JobStateLeased)
	}

	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem && s.State != SubsystemRunning {
			t.Fatalf("jobs.scheduler subsystem state = %v, want Running (detail: %s)", s.State, s.Detail)
		}
	}
}

// TestRegisterScheduler_ResumeSkippedOnNonController proves a node-role
// daemon's Resume refusal (R-21.169) does not fail RegisterScheduler: the
// subsystem still starts, and the seeded job is left untouched (still
// `running`), because a node daemon never resumes -- only the controller
// does.
func TestRegisterScheduler_ResumeSkippedOnNonController(t *testing.T) {
	store := newDaemonTestResumeStore(t)
	js := journal.New(storetest.NewMemStore(), runtime.NewFixedClock(fixedResumeNow), journal.DefaultNamespace)
	seedStaleRunningJob(t, store, js, "resume-job-2")

	bus := newSchedulerTestBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManifest(nil, runtime.NewFixedClock(fixedResumeNow))
	// fakeBindingBackend{ok: true} resolves nodes.ResolveRole to RoleNode
	// (subsystems_scheduler_test.go's own precedent).
	sched, err := m.RegisterScheduler(ctx, bus, runtime.NewFixedClock(fixedResumeNow), fakeBindingBackend{ok: true}, "sched-test-resume-node", store, js, 0)
	if err != nil {
		t.Fatalf("RegisterScheduler on a node daemon: %v (want no error -- resume is skipped, not fatal)", err)
	}
	if sched == nil {
		t.Fatal("RegisterScheduler returned a nil Scheduler")
	}

	got, ok, err := store.GetJob(context.Background(), "resume-job-2")
	if err != nil || !ok {
		t.Fatalf("GetJob(resume-job-2) after RegisterScheduler: ok=%v err=%v", ok, err)
	}
	if got.State != jobs.JobStateRunning {
		t.Fatalf("job state in store = %q, want %q (a node daemon must never apply the resume-only edge)", got.State, jobs.JobStateRunning)
	}

	found := false
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem {
			found = true
			if s.State != SubsystemRunning {
				t.Fatalf("jobs.scheduler subsystem state = %v, want Running", s.State)
			}
		}
	}
	if !found {
		t.Fatalf("Manifest snapshot has no %q entry: %+v", jobSchedulerSubsystem, m.Snapshot())
	}
}
