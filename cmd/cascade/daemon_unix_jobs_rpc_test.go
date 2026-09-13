//go:build !windows

// Purpose: composition-root proof for wireJobRPC and the four adapter
//
//	types daemon_unix_jobs_rpc_adapters.go builds (jobStoreAdapter,
//	leaseStoreAdapter, controllerGuardAdapter, outboxAdapter). These
//	shipped with zero direct callers: wireJobRPC is invoked only from
//	wireJobRPCHandlers (buildRPCServer's own composition root, exercised
//	only by full-daemon integration tests this package does not run at
//	the unit level), so every adapter method sat at 0% coverage. This
//	file dispatches job.list/job.show/job.cancel/job.retry against the
//	REAL registry wireJobRPC builds, over a real sqlite cascade.db under
//	t.TempDir() -- proving the wiring end to end, never a "no panic"
//	smoke test.
//
// SPORT: cmd/cascade/daemon:jobs-rpc-adapters (TEST) -- P1-E29-W6-S60-T1.
package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// setupJobsRPC wires a real job.*/lease.* RPC surface over a fresh
// cascade.db under t.TempDir(), returning the registry to dispatch
// against and the *jobs.Store this test seeds fixtures through directly
// (the same db handle wireJobRPC opened, per its own doc comment on
// sharing one sqlite file across composition-root subsystems).
func setupJobsRPC(t *testing.T) (*rpc.Registry, *jobs.Store) {
	t.Helper()
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	registry := rpc.NewRegistry()

	db, err := wireJobRPC(context.Background(), registry, paths, clock, rpc.NewNonceLedger(clock), rpc.MapTrustStore{})
	if err != nil {
		t.Fatalf("wireJobRPC: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return registry, jobs.NewStore(db)
}

func TestWireJobRPC_MountsAllSixMethods(t *testing.T) {
	registry, _ := setupJobsRPC(t)
	for _, method := range []string{"job.list", "job.show", "job.cancel", "job.retry", "lease.list", "lease.release"} {
		if !registry.Registered(method) {
			t.Errorf("registry.Registered(%q) = false, want true", method)
		}
	}
}

func seedTestJob(t *testing.T, store *jobs.Store, id string, state jobs.JobState) {
	t.Helper()
	err := store.PutJob(context.Background(), jobs.Job{
		ID: id, State: state, CreatedAt: 1, UpdatedAt: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	})
	if err != nil {
		t.Fatalf("PutJob(%s): %v", id, err)
	}
}

// TestJobList_ReturnsRealSeededJob proves handleJobList reaches
// jobStoreAdapter.ListJobs -> jobRecordFrom against a real store row.
func TestJobList_ReturnsRealSeededJob(t *testing.T) {
	registry, store := setupJobsRPC(t)
	seedTestJob(t, store, "job-list-1", jobs.JobStatePending)

	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "job.list", Params: json.RawMessage(`{}`)})
	if errObj != nil {
		t.Fatalf("job.list dispatch: %+v", errObj)
	}
	page, ok := result.(rpc.JobPage)
	if !ok {
		t.Fatalf("job.list result type = %T, want rpc.JobPage", result)
	}
	found := false
	for _, j := range page.Jobs {
		if j.ID == "job-list-1" {
			found = true
			if j.State != "pending" {
				t.Errorf("job-list-1 State = %q, want \"pending\"", j.State)
			}
		}
	}
	if !found {
		t.Errorf("job.list did not return the seeded job: %+v", page.Jobs)
	}
}

// TestJobCancel_TransitionsAndConfirmsOutbox drives job.cancel end to
// end: guard check (controllerGuardAdapter), GetJob/CancelJob
// (jobStoreAdapter), and DeriveKey/RecordIntent/Confirm (outboxAdapter)
// all run against real collaborators over the real cascade.db.
func TestJobCancel_TransitionsAndConfirmsOutbox(t *testing.T) {
	registry, store := setupJobsRPC(t)
	seedTestJob(t, store, "job-cancel-1", jobs.JobStatePending)

	params, _ := json.Marshal(map[string]string{"id": "job-cancel-1"})
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "job.cancel", Params: params})
	if errObj != nil {
		t.Fatalf("job.cancel dispatch: %+v", errObj)
	}
	j, ok, err := store.GetJob(context.Background(), "job-cancel-1")
	if err != nil || !ok {
		t.Fatalf("GetJob after cancel: ok=%v err=%v", ok, err)
	}
	if j.State != jobs.JobStateCancelling {
		t.Errorf("job-cancel-1 State after job.cancel = %q, want %q", j.State, jobs.JobStateCancelling)
	}
}

// TestJobRetry_CreatesNewPendingJob drives job.retry end to end: a
// failed job's retry writes a NEW pending row (jobStoreAdapter.RetryJob)
// through the same outbox intent/confirm cycle job.cancel uses.
func TestJobRetry_CreatesNewPendingJob(t *testing.T) {
	registry, store := setupJobsRPC(t)
	seedTestJob(t, store, "job-retry-1", jobs.JobStateFailed)

	params, _ := json.Marshal(map[string]string{"id": "job-retry-1"})
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "job.retry", Params: params})
	if errObj != nil {
		t.Fatalf("job.retry dispatch: %+v", errObj)
	}
	newJob, ok := result.(rpc.JobRecord)
	if !ok {
		t.Fatalf("job.retry result type = %T, want rpc.JobRecord", result)
	}
	if newJob.State != "pending" {
		t.Errorf("retried job State = %q, want \"pending\"", newJob.State)
	}
	if newJob.ID == "job-retry-1" {
		t.Error("job.retry reused the original job id, want a new one")
	}
	stored, ok, err := store.GetJob(context.Background(), newJob.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(new retry id): ok=%v err=%v", ok, err)
	}
	if stored.ConsecutiveFailedAttempts != 1 {
		t.Errorf("retried job ConsecutiveFailedAttempts = %d, want 1", stored.ConsecutiveFailedAttempts)
	}
}

// TestJobShow_NotFoundRefuses proves handleJobShow's not-found path
// reaches jobStoreAdapter.GetJob's ok=false branch cleanly.
func TestJobShow_NotFoundRefuses(t *testing.T) {
	registry, _ := setupJobsRPC(t)
	params, _ := json.Marshal(map[string]string{"id": "does-not-exist"})
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "job.show", Params: params})
	if errObj == nil {
		t.Fatal("job.show on a missing id = nil error, want a not-found refusal")
	}
}

func TestLeaseList_EmptyStoreReturnsEmptyPage(t *testing.T) {
	registry, _ := setupJobsRPC(t)
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "lease.list", Params: json.RawMessage(`{}`)})
	if errObj != nil {
		t.Fatalf("lease.list dispatch: %+v", errObj)
	}
	page, ok := result.(rpc.LeasePage)
	if !ok {
		t.Fatalf("lease.list result type = %T, want rpc.LeasePage", result)
	}
	if len(page.Leases) != 0 {
		t.Errorf("lease.list on an empty store = %d leases, want 0", len(page.Leases))
	}
}

// TestLeaseRelease_ReleasesRealAcquiredLease drives lease.list and
// lease.release against a lease acquired through a real
// *jobs.LeaseManager over the SAME db wireJobRPC opened, proving
// leaseStoreAdapter's ListLeases/GetLease/ReleaseLease and
// leaseRecordFrom all reach the real store rows.
func TestLeaseRelease_ReleasesRealAcquiredLease(t *testing.T) {
	registry, store := setupJobsRPC(t)
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	leaseMgr := jobs.NewLeaseManager(store, clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)

	acquired, err := leaseMgr.Acquire(context.Background(), "repo1", "src/**", "job-holder-1")
	if err != nil || !acquired.Granted {
		t.Fatalf("Acquire: granted=%v err=%v", acquired.Granted, err)
	}
	leaseID := jobs.LeaseID(acquired.Lease)

	listResult, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "lease.list", Params: json.RawMessage(`{}`)})
	if errObj != nil {
		t.Fatalf("lease.list dispatch: %+v", errObj)
	}
	page := listResult.(rpc.LeasePage)
	if len(page.Leases) != 1 || page.Leases[0].Holder != "job-holder-1" {
		t.Fatalf("lease.list = %+v, want one lease held by job-holder-1", page.Leases)
	}

	releaseParams, _ := json.Marshal(map[string]string{"id": leaseID, "as_job": "job-holder-1"})
	_, errObj = registry.Dispatch(context.Background(), &rpc.Request{Method: "lease.release", Params: releaseParams})
	if errObj != nil {
		t.Fatalf("lease.release dispatch: %+v", errObj)
	}

	released, ok, err := store.GetLeaseByID(context.Background(), leaseID)
	if err != nil || !ok {
		t.Fatalf("GetLeaseByID after release: ok=%v err=%v", ok, err)
	}
	if released.State != "released" {
		t.Errorf("lease State after lease.release = %q, want \"released\"", released.State)
	}
}
