package rpc

// Purpose: table-driven tests for RegisterJobHandlers, dispatched through
//	a real *Registry (the D/S-06.T4 dispatcher this ticket registers
//	against) with an in-memory fake JobStore/LeaseStore/OutboxRecorder/
//	ControllerGuard — this package cannot construct a real *jobs.Store
//	without importing internal/jobs, which is the import cycle jobs.go's
//	CONTRACT NOTE proves. The fakes implement exactly the seams
//	RegisterJobHandlers depends on; internal/jobs/query_test.go already
//	covers the real Store-backed ListJobs/ListLeases logic those seams
//	wrap in production (cmd/cascade's adapter, out of this package).
//
// SPORT: rpc/job.* methods (ADD) (P1-E29-W6-S60-T1).

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeJobStore is an in-memory JobStore.
type fakeJobStore struct {
	jobs map[string]JobRecord
}

func newFakeJobStore(jobs ...JobRecord) *fakeJobStore {
	m := map[string]JobRecord{}
	for _, j := range jobs {
		m[j.ID] = j
	}
	return &fakeJobStore{jobs: m}
}

func (f *fakeJobStore) ListJobs(_ context.Context, q JobListQuery) (JobPage, error) {
	var out []JobRecord
	for _, j := range f.jobs {
		if q.State != "" && j.State != q.State {
			continue
		}
		out = append(out, j)
	}
	return JobPage{Jobs: out}, nil
}

func (f *fakeJobStore) GetJob(_ context.Context, id string) (JobRecord, bool, error) {
	j, ok := f.jobs[id]
	return j, ok, nil
}

func (f *fakeJobStore) CancelJob(_ context.Context, id string, now int64) (JobRecord, error) {
	j := f.jobs[id]
	j.State = "cancelling"
	j.UpdatedAt = now
	f.jobs[id] = j
	return j, nil
}

func (f *fakeJobStore) RetryJob(_ context.Context, orig JobRecord, newID string, gen int64, now int64) (JobRecord, error) {
	nj := orig
	nj.ID = newID
	nj.State = "pending"
	nj.CreatedAt = now
	nj.UpdatedAt = now
	nj.ConsecutiveFailedAttempts = gen
	f.jobs[newID] = nj
	return nj, nil
}

// fakeLeaseStore is an in-memory LeaseStore.
type fakeLeaseStore struct {
	leases map[string]LeaseRecord
}

func newFakeLeaseStore(leases ...LeaseRecord) *fakeLeaseStore {
	m := map[string]LeaseRecord{}
	for _, l := range leases {
		m[l.ID] = l
	}
	return &fakeLeaseStore{leases: m}
}

func (f *fakeLeaseStore) ListLeases(_ context.Context, _ LeaseListQuery) (LeasePage, error) {
	var out []LeaseRecord
	for _, l := range f.leases {
		out = append(out, l)
	}
	return LeasePage{Leases: out}, nil
}

func (f *fakeLeaseStore) GetLease(_ context.Context, id string) (LeaseRecord, bool, error) {
	l, ok := f.leases[id]
	return l, ok, nil
}

func (f *fakeLeaseStore) ReleaseLease(_ context.Context, id string) error {
	l, ok := f.leases[id]
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "lease %q not found", id)
	}
	l.State = "released"
	f.leases[id] = l
	return nil
}

// fakeOutbox is an in-memory OutboxRecorder recording every call, so
// tests can assert exactly-once behavior.
type fakeOutbox struct {
	confirmed map[string]bool
	intents   int
	confirms  int
}

func newFakeOutbox() *fakeOutbox { return &fakeOutbox{confirmed: map[string]bool{}} }

func (f *fakeOutbox) DeriveKey(jobID string, gen int64, hash string) string {
	return fmt.Sprintf("integration:%s:%d:%s", jobID, gen, hash)
}

func (f *fakeOutbox) RecordIntent(_ context.Context, _ string, _ int64, _ string) error {
	f.intents++
	return nil
}

func (f *fakeOutbox) Confirm(_ context.Context, key string) error {
	f.confirms++
	f.confirmed[key] = true
	return nil
}

// allowAllGuard is a ControllerGuard that never refuses (this daemon is
// the controller).
type allowAllGuard struct{}

func (allowAllGuard) RequireController(string) error { return nil }

// refuseGuard is a ControllerGuard that always refuses (node daemon).
type refuseGuard struct{}

func (refuseGuard) RequireController(method string) error {
	return cascade.Newf(cascade.KindPermissionDenied, "refused on a node daemon: %s", method)
}

func testJobDeps(jobs *fakeJobStore, leases *fakeLeaseStore, guard ControllerGuard, clock runtime.Clock) JobHandlerDeps {
	return JobHandlerDeps{
		Jobs: jobs, Leases: leases, Guard: guard, Outbox: newFakeOutbox(),
		Ledger: NewNonceLedger(clock), Trust: MapTrustStore{}, Clock: clock,
	}
}

func dispatchJSON(t *testing.T, registry *Registry, method string, params any) (json.RawMessage, *ErrorObject) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &Request{Method: method, Params: raw})
	if errObj != nil {
		return nil, errObj
	}
	out, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return out, nil
}

func TestJobList_FiltersByState(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	store := newFakeJobStore(JobRecord{ID: "j1", State: "pending"}, JobRecord{ID: "j2", State: "failed"})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(store, newFakeLeaseStore(), allowAllGuard{}, clock))

	raw, errObj := dispatchJSON(t, registry, "job.list", map[string]any{"state": "failed"})
	if errObj != nil {
		t.Fatalf("job.list: %v", errObj)
	}
	var page JobPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Jobs) != 1 || page.Jobs[0].ID != "j2" {
		t.Fatalf("job.list(state=failed) = %+v", page.Jobs)
	}
}

func TestJobShow_UnknownIDTypedError(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(newFakeJobStore(), newFakeLeaseStore(), allowAllGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "job.show", map[string]any{"id": "missing"})
	if errObj == nil {
		t.Fatal("expected a typed error for an unknown job id")
	}
}

func TestJobCancel_NonTerminalTransitionsToCancelling(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	store := newFakeJobStore(JobRecord{ID: "j1", State: "running"})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(store, newFakeLeaseStore(), allowAllGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "job.cancel", map[string]any{"id": "j1"})
	if errObj != nil {
		t.Fatalf("job.cancel: %v", errObj)
	}
	j, _ := store.jobs["j1"]
	if j.State != "cancelling" {
		t.Fatalf("state = %q, want cancelling", j.State)
	}
}

func TestJobCancel_AlreadyTerminalIsIdempotentSuccess(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	store := newFakeJobStore(JobRecord{ID: "j1", State: "accepted"})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(store, newFakeLeaseStore(), allowAllGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "job.cancel", map[string]any{"id": "j1"})
	if errObj != nil {
		t.Fatalf("job.cancel on terminal job must succeed idempotently: %v", errObj)
	}
	if store.jobs["j1"].State != "accepted" {
		t.Fatalf("terminal job state must not change, got %q", store.jobs["j1"].State)
	}
}

func TestJobCancel_RefusedOnNodeDaemon(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	store := newFakeJobStore(JobRecord{ID: "j1", State: "running"})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(store, newFakeLeaseStore(), refuseGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "job.cancel", map[string]any{"id": "j1"})
	if errObj == nil {
		t.Fatal("expected job.cancel to be refused on a node daemon")
	}
}

func TestJobRetry_FailedCreatesNewJob(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	store := newFakeJobStore(JobRecord{ID: "j1", State: "failed", ConsecutiveFailedAttempts: 2})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(store, newFakeLeaseStore(), allowAllGuard{}, clock))

	raw, errObj := dispatchJSON(t, registry, "job.retry", map[string]any{"id": "j1"})
	if errObj != nil {
		t.Fatalf("job.retry: %v", errObj)
	}
	var nj JobRecord
	if err := json.Unmarshal(raw, &nj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if nj.ID == "j1" || nj.State != "pending" || nj.ConsecutiveFailedAttempts != 3 {
		t.Fatalf("retry result = %+v", nj)
	}
	if _, ok := store.jobs["j1"]; !ok {
		t.Fatal("original failed job must not be re-opened or removed")
	}
}

func TestJobRetry_CancelledRefusedWithErrNotRetryable(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	store := newFakeJobStore(JobRecord{ID: "j1", State: "cancelled"})
	registry := NewRegistry()
	RegisterJobHandlers(registry, testJobDeps(store, newFakeLeaseStore(), allowAllGuard{}, clock))

	_, errObj := dispatchJSON(t, registry, "job.retry", map[string]any{"id": "j1"})
	if errObj == nil {
		t.Fatal("expected ErrNotRetryable for a CANCELLED job")
	}
}
