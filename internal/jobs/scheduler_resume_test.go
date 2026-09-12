package jobs

// Purpose: Resume's staleness scan and the heartbeat reaper. The HOW 3
// kill -9 mid-DAG test (a REAL child OS process, REAL SIGKILLed) lives in
// scheduler_resume_kill9_test.go, split out to keep both files under the
// 300-line cap.
//
// SPORT: jobs/scheduler/ADD (tests) (P1-E29-W6-S59-T5); FIX R-16.82.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

func TestResume_RefusesOnNonController(t *testing.T) {
	sched := NewScheduler(runtime.NewFixedClock(time.Unix(0, 0)))
	_, err := sched.Resume(nodeCtx(), newResumeTestStore(t), nil, 0, nil, nil)
	if err == nil {
		t.Fatal("Resume on a node daemon: err = nil, want a refusal")
	}
}

func TestResume_ReentersStaleRunningJobAtLeased(t *testing.T) {
	store := newResumeTestStore(t)
	now := int64(10_000)
	seedRunningJobWithLease(t, store, "j1", now-8000, LeaseHeld) // ttl+grace = 7800s < 8000s elapsed: stale

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(now, 0)))
	events, err := sched.Resume(controllerCtx(), store, alwaysHasEntriesReader{}, 0, nil, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want exactly one re-entry", events)
	}
	jt, ok := events[0].(JobTransitioned)
	if !ok || jt.JobID != "j1" || jt.From != JobStateRunning || jt.To != JobStateLeased {
		t.Fatalf("events[0] = %#v, want JobTransitioned{j1, running->leased}", events[0])
	}

	// R-16.82 / DEFECT-resume-transition-not-applied: the emitted event is
	// not proof of anything -- assert the job's state IN THE STORE.
	got, ok, err := store.GetJob(context.Background(), "j1")
	if err != nil || !ok {
		t.Fatalf("GetJob(j1) after Resume: ok=%v err=%v", ok, err)
	}
	if got.State != JobStateLeased {
		t.Fatalf("job state in store = %q, want %q (event alone is not application)", got.State, JobStateLeased)
	}
}

func TestResume_LeavesJobWithinGraceWindowRunning(t *testing.T) {
	store := newResumeTestStore(t)
	now := int64(10_000)
	seedRunningJobWithLease(t, store, "j1", now-10, LeaseHeld) // barely issued: within grace

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(now, 0)))
	events, err := sched.Resume(controllerCtx(), store, alwaysHasEntriesReader{}, 0, nil, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none: job is within the grace window", events)
	}
}

func TestResume_SkipsExpiredUnconfirmedLease(t *testing.T) {
	store := newResumeTestStore(t)
	now := int64(10_000)
	seedRunningJobWithLease(t, store, "j1", now-8000, LeaseExpiredUnconfirmed)

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(now, 0)))
	events, err := sched.Resume(controllerCtx(), store, alwaysHasEntriesReader{}, 0, nil, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none: R-21.139 forbids re-entry until reclaim confirms death", events)
	}

	// The store-level guard (holderHasExpiredUnconfirmedLeaseTx, checked
	// AT the transition, not merely upstream) must independently refuse
	// this job's state too, not just the caller's own pre-check.
	got, ok, err := store.GetJob(context.Background(), "j1")
	if err != nil || !ok {
		t.Fatalf("GetJob(j1): ok=%v err=%v", ok, err)
	}
	if got.State != JobStateRunning {
		t.Fatalf("job state in store = %q, want still %q: expired_unconfirmed must not resume", got.State, JobStateRunning)
	}
}

func TestResume_SkipsReentryWithNoJournalTrail(t *testing.T) {
	store := newResumeTestStore(t)
	now := int64(10_000)
	seedRunningJobWithLease(t, store, "j1", now-8000, LeaseHeld)

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(now, 0)))
	events, err := sched.Resume(controllerCtx(), store, emptyReader{}, 0, nil, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none: no journal trail for this job", events)
	}
}

func TestHeartbeatReaper_AbandonsStaleExecution(t *testing.T) {
	store := newResumeTestStore(t)
	ctx := context.Background()
	if err := store.PutJob(ctx, baseJob("j1")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutExecution(ctx, Execution{ID: "e1", JobID: "j1", Attempt: 1, State: ExecutionRunning, StartedAt: 1, HeartbeatAt: 100}); err != nil {
		t.Fatalf("seed execution: %v", err)
	}

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(1000, 0)))
	_, err := sched.Resume(controllerCtx(), store, alwaysHasEntriesReader{}, time.Minute, nil, nil) // 3*60s = 180s window; heartbeat at 100, now 1000: stale
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	exec, ok, err := store.GetExecution(ctx, "e1")
	if err != nil || !ok {
		t.Fatalf("GetExecution: %v %v", ok, err)
	}
	if exec.State != ExecutionAbandoned {
		t.Fatalf("execution state = %q, want abandoned", exec.State)
	}
}

// newResumeTestStore is newTestStore plus the outbox schema Resume's
// reconciliation step always touches.
func newResumeTestStore(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	if err := ApplyOutboxSchema(context.Background(), store.db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	return store
}

// seedRunningJobWithLease puts a `running` job holding a lease issued at
// issuedAt in leaseState.
func seedRunningJobWithLease(t *testing.T, store *Store, jobID string, issuedAt int64, leaseState LeaseState) {
	t.Helper()
	ctx := context.Background()
	j := baseJob(jobID)
	j.State = JobStateRunning
	if err := store.PutJob(ctx, j); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutLease(ctx, ResourceLease{
		RepoID: "repo-1", ScopeGlob: "a/**", Holder: jobID, IssuedAt: issuedAt,
		TTLSeconds: DefaultLeaseDefaults().TTLSeconds, Epoch: 1, State: leaseState,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
}
