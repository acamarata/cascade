// Purpose: error-path and edge-case coverage for job.go/lock.go/
//   scheduler.go's validation, Store-failure, and corrupt-record branches
//   that the behavioral tests in the other files don't happen to exercise
//   (Article 4's 85%/80% core-engine coverage floor). R-14.117-authorized
//   split of scheduler_test.go.
// SPORT: internal.events.scheduler/ADDED (error-path tests)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failingStore wraps a real MemStore and injects a cascade.KindUnavailable
// error from whichever methods are flagged, so job.go/lock.go's
// Store-failure branches (unreachable via a real, always-succeeding
// MemStore) are exercised directly.
type failingStore struct {
	*storetest.MemStore
	failGet  bool
	failPut  bool
	failScan bool
}

func (f *failingStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if f.failGet {
		return nil, cascade.New(cascade.KindUnavailable, "injected Get failure")
	}
	return f.MemStore.Get(ctx, namespace, key)
}

func (f *failingStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if f.failPut {
		return cascade.New(cascade.KindUnavailable, "injected Put failure")
	}
	return f.MemStore.Put(ctx, namespace, key, value)
}

func (f *failingStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	if f.failScan {
		return nil, cascade.New(cascade.KindUnavailable, "injected Scan failure")
	}
	return f.MemStore.Scan(ctx, namespace, prefix)
}

func TestJobStore_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	fs := &failingStore{MemStore: storetest.NewMemStore()}

	fs.failPut = true
	if err := putJob(ctx, fs, testNamespace, CronJob{ID: "j1", Spec: "@every 1h", Owner: "o"}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("putJob with failing Put = %v, want KindUnavailable", err)
	}
	fs.failPut = false

	fs.failGet = true
	if _, err := getJob(ctx, fs, testNamespace, "j1"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("getJob with failing Get = %v, want KindUnavailable", err)
	}
	fs.failGet = false

	fs.failScan = true
	if _, err := listJobs(ctx, fs, testNamespace); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("listJobs with failing Scan = %v, want KindUnavailable", err)
	}
}

func TestDecodeJob_Corrupt(t *testing.T) {
	if _, err := decodeJob([]byte("not json")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeJob(malformed json) = %v, want KindIntegrity", err)
	}
	if _, err := decodeJob([]byte(`{"id":"j1","spec":"@every 1h","owner":"o","last_fire":"not-a-time"}`)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeJob(bad last_fire) = %v, want KindIntegrity", err)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	store := storetest.NewMemStore()
	_, err := getJob(context.Background(), store, testNamespace, "missing")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("getJob(missing) = %v, want KindNotFound", err)
	}
}

func TestSchedulerValidate_MissingFields(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	ctx := context.Background()

	cases := []*Scheduler{
		New(nil, testNamespace, clock, bus, "o", time.Hour),
		New(store, "", clock, bus, "o", time.Hour),
		New(store, testNamespace, nil, bus, "o", time.Hour),
		New(store, testNamespace, clock, nil, "o", time.Hour),
		New(store, testNamespace, clock, bus, "", time.Hour),
		New(store, testNamespace, clock, bus, "o", 0),
	}
	for i, sched := range cases {
		if _, err := sched.Activate(ctx); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("case %d: Activate() = %v, want KindInvalidInput", i, err)
		}
	}
}

func TestSchedulerRegisterRunnable_Validation(t *testing.T) {
	sched, _, _, _ := newTestScheduler(t, "o")
	if err := sched.RegisterRunnable("", func(context.Context) error { return nil }); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RegisterRunnable(empty owner) = %v, want KindInvalidInput", err)
	}
	if err := sched.RegisterRunnable("owner", nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RegisterRunnable(nil fn) = %v, want KindInvalidInput", err)
	}
}

func TestSchedulerScheduleJob_Validation(t *testing.T) {
	sched, _, _, _ := newTestScheduler(t, "o")
	ctx := context.Background()
	if err := sched.ScheduleJob(ctx, "", "@every 1h", "owner"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("ScheduleJob(empty id) = %v, want KindInvalidInput", err)
	}
	if err := sched.ScheduleJob(ctx, "id", "@every 1h", ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("ScheduleJob(empty owner) = %v, want KindInvalidInput", err)
	}
}

func TestAdvisoryLock_ReacquireBySameOwner(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	l := newAdvisoryLock(store, testNamespace, "owner-a", time.Hour, clock)
	ctx := context.Background()
	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("re-Acquire by the same owner: %v, want success (idempotent)", err)
	}
}

func TestAdvisoryLock_RenewNeverAcquired(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	l := newAdvisoryLock(store, testNamespace, "owner-a", time.Hour, clock)
	if err := l.Renew(context.Background()); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Renew without a prior Acquire = %v, want KindConflict", err)
	}
}

func TestAdvisoryLock_ReleaseNoop(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	l := newAdvisoryLock(store, testNamespace, "owner-a", time.Hour, clock)
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("Release without a prior Acquire = %v, want a safe no-op", err)
	}
}

func TestAdvisoryLock_ReleaseAfterStolen(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	ctx := context.Background()
	a := newAdvisoryLock(store, testNamespace, "owner-a", time.Hour, clock)
	if err := a.Acquire(ctx); err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	clock.Advance(2 * time.Hour) // expire a's lease
	b := newAdvisoryLock(store, testNamespace, "owner-b", time.Hour, clock)
	if err := b.Acquire(ctx); err != nil {
		t.Fatalf("b.Acquire (stealing expired lease): %v", err)
	}
	// a's Release must be a no-op now — it no longer owns the lock, and
	// must never release what b legitimately holds.
	if err := a.Release(ctx); err != nil {
		t.Fatalf("a.Release after being stolen = %v, want a safe no-op", err)
	}
	if err := b.Renew(ctx); err != nil {
		t.Fatalf("b.Renew after a's no-op Release = %v, want success (b still holds it)", err)
	}
}
