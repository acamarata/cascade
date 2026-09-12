//go:build !windows

// Purpose: proves wireJobScheduler's Resume call (daemon_unix_scheduler_dag.go)
//
//	actually runs from buildRPCServer -- the SAME composition root
//	platformDaemonRun calls in production, and the same "never call the
//	registrar directly" posture daemon_unix_scheduler_dag_test.go already
//	sets -- so a `running` job left behind by a prior crash is re-entered
//	at `leased` IN THE STORE before this daemon ever answers an RPC.
//	DEFECT-scheduler-resume-never-called's fix.
//
// SPORT: cmd/cascade/daemon (FIX, DEFECT-scheduler-resume-never-called).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fixedSchedulerResumeE2ENow is this file's injected "now" (Art.7.1: no
// bare time.Now). IssuedAt=1 plus TTL+grace (7800s) is long past by this
// instant.
var fixedSchedulerResumeE2ENow = time.Unix(10_000, 0)

// seedStaleRunningJobDB opens its own connection to the SAME
// paths.DataDir()/cascade.db file wireJobScheduler's
// openSchedulerResumeJobsStore reads, applies the real jobs+outbox
// schema, and puts a `running` job holding a stale lease -- mirroring
// internal/daemon/subsystems_scheduler_resume_test.go's seed helper, one
// level up at the real daemon entry point.
func seedStaleRunningJobDB(t *testing.T, paths fakeMemoryPaths, id string) {
	t.Helper()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll DataDir: %v", err)
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	clock := runtime.NewFixedClock(fixedSchedulerResumeE2ENow)
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyOutboxSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	store := jobs.NewStore(db)
	ctx := context.Background()
	job := jobs.Job{
		ID: id, State: jobs.JobStateRunning, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "repo:/tmp/x", RiskClass: "normal",
		MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1.0,
		Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}
	if err := store.PutJob(ctx, job); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutLease(ctx, jobs.ResourceLease{
		RepoID: "repo-1", ScopeGlob: "a/**", Holder: id, IssuedAt: 1,
		TTLSeconds: jobs.DefaultLeaseDefaults().TTLSeconds, Epoch: 1, State: jobs.LeaseHeld,
	}); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
}

// getJobStateFromDB reads id's current state back from the SAME
// cascade.db file, a fresh connection distinct from wireJobScheduler's
// own (which it closes before buildRPCServer returns) -- proving the
// state landed IN THE STORE, not merely in an in-memory Scheduler value.
func getJobStateFromDB(t *testing.T, paths fakeMemoryPaths, id string) jobs.JobState {
	t.Helper()
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open (read-back): %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	store := jobs.NewStore(db)
	job, ok, err := store.GetJob(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s) read-back: ok=%v err=%v", id, ok, err)
	}
	return job.State
}

// TestBuildRPCServer_ResumesStaleRunningJobAtLeased is the mutation-tested
// proof: with wireJobScheduler's Resume wiring present, this test is
// GREEN. Removing that wiring (this ticket's journal records the exact
// mutation and its RED output) turns it RED with the job still `running`
// in the store -- the DEFECT-scheduler-resume-never-called symptom
// exactly.
func TestBuildRPCServer_ResumesStaleRunningJobAtLeased(t *testing.T) {
	clock := runtime.NewFixedClock(fixedSchedulerResumeE2ENow)
	paths := fakeMemoryPaths{root: t.TempDir()}
	seedStaleRunningJobDB(t, paths, "e2e-resume-job-1")

	sharedStore := storetest.NewMemStore()
	js := journal.New(sharedStore, clock, journal.DefaultNamespace)
	if _, err := js.Append(context.Background(), "e2e-resume-job-1", journal.KindIntent, "seed", []byte(`{}`)); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}

	bus := events.New(sharedStore, clock)
	srv, manifest, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: filepath.Join(paths.root, "d.sock")}, paths, nil, sharedStore)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	if srv == nil {
		t.Fatal("buildRPCServer returned a nil server")
	}

	for _, s := range manifest.Snapshot() {
		if s.Name == "jobs.scheduler" && s.State != daemon.SubsystemRunning {
			t.Fatalf("jobs.scheduler subsystem state = %v, want Running (detail: %s)", s.State, s.Detail)
		}
	}

	if got := getJobStateFromDB(t, paths, "e2e-resume-job-1"); got != jobs.JobStateLeased {
		t.Fatalf("job state in store after buildRPCServer = %q, want %q: the real daemon entry point never resumed it", got, jobs.JobStateLeased)
	}
}
