package jobs

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// alwaysController is the test isController func for every test that
// does not itself exercise R-21.169's node-daemon refusal.
func alwaysController() bool { return true }

// newTestLeaseManager returns a LeaseManager over a fresh real db, with
// a *runtime.FixedClock the caller can Advance, and no sink wiring
// (pure store-level tests; lease_events_test.go covers the real
// journal/bus/attention integration).
func newTestLeaseManager(t *testing.T) (*LeaseManager, *runtime.FixedClock, *Store) {
	t.Helper()
	store := newTestStore(t)
	clock := runtime.NewFixedClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	return NewLeaseManager(store, clock, alwaysController, DefaultLeaseDefaults(), nil), clock, store
}

// TestLeaseAcquireMutualExclusion is the ONE-MUTABLE-WRITER proof under
// REAL concurrency: N goroutines race Acquire for the SAME intersecting
// scope simultaneously (a barrier holds every goroutine at the starting
// line so they genuinely overlap, not a sequential loop that would prove
// nothing about mutual exclusion). Exactly one must observe Granted;
// every other must observe Contended naming the winner's lease. Run with
// -race.
func TestLeaseAcquireMutualExclusion(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()
	const n = 16

	var start sync.WaitGroup
	start.Add(1)
	var ready, done sync.WaitGroup
	ready.Add(n)
	done.Add(n)

	results := make(chan AcquireResult, n) // buffered: every send has room, no goroutine blocks on it
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		holder := fmt.Sprintf("job-%d", i)
		go func() {
			defer done.Done()
			ready.Done()
			start.Wait() // every goroutine releases at the same instant
			res, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", holder)
			results <- res
			errs <- err
		}()
	}
	ready.Wait() // every goroutine is past setup and blocked on the barrier
	start.Done() // release them all at once

	done.Wait()
	close(results)
	close(errs)

	grantedCount := 0
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Acquire returned an error instead of a contended result: %v", err)
		}
	}
	var winner AcquireResult
	for res := range results {
		if res.Granted {
			grantedCount++
			winner = res
		}
	}
	if grantedCount != 1 {
		t.Fatalf("mutual exclusion violated: %d of %d concurrent intersecting Acquire calls were granted, want exactly 1", grantedCount, n)
	}
	if winner.Lease.RepoID != "repo-1" || winner.Lease.State != LeaseHeld {
		t.Errorf("winning lease = %+v, want a held repo-1 lease", winner.Lease)
	}
}

// TestLeaseAcquireNonIntersectingParallel proves the converse under the
// SAME -race concurrency: two disjoint scopes both grant, in parallel,
// with no serialization between them beyond the DB's own transaction
// boundary.
func TestLeaseAcquireNonIntersectingParallel(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make(chan AcquireResult, 2)
	errs := make(chan error, 2)
	scopes := []string{"internal/jobs/**", "internal/nodes/**"}
	wg.Add(2)
	for i, scope := range scopes {
		holder := fmt.Sprintf("job-%d", i)
		s := scope
		go func() {
			defer wg.Done()
			res, err := m.Acquire(ctx, "repo-1", s, holder)
			results <- res
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}
	for res := range results {
		if !res.Granted {
			t.Errorf("non-intersecting Acquire returned Contended (against %+v), want both granted", res.Contending)
		}
	}
}

// TestLeaseAcquireContendedNamesConflict constructs the actual conflict
// (Art.2's proof-the-negative requirement: a detector run only against
// non-conflicting input proves nothing) and asserts it is caught with
// the conflicting lease correctly named.
func TestLeaseAcquireContendedNamesConflict(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()

	first, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !first.Granted {
		t.Fatalf("first Acquire = %+v, err=%v, want granted", first, err)
	}
	second, err := m.Acquire(ctx, "repo-1", "internal/jobs/lease.go", "job-b")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if second.Granted {
		t.Fatalf("second Acquire on an intersecting scope was granted, want Contended against job-a's lease")
	}
	if second.Contending.Holder != "job-a" {
		t.Errorf("Contending.Holder = %q, want %q", second.Contending.Holder, "job-a")
	}

	// Disjoint scope must NOT be reported as contended -- proves the
	// detector distinguishes conflict from non-conflict, not just
	// "always refuses the second call".
	third, err := m.Acquire(ctx, "repo-1", "internal/nodes/**", "job-c")
	if err != nil || !third.Granted {
		t.Fatalf("disjoint-scope Acquire = %+v, err=%v, want granted", third, err)
	}
}

// TestLeaseReleaseThenReacquire proves release frees the scope, and that
// the epoch strictly advances across the release/reacquire boundary
// (R-21.139 monotonicity survives a full cycle).
func TestLeaseReleaseThenReacquire(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()

	first, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !first.Granted {
		t.Fatalf("Acquire: %+v, %v", first, err)
	}
	if err := m.Release(ctx, "repo-1", first.Lease.ScopeGlob, first.Lease.Epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-b")
	if err != nil || !second.Granted {
		t.Fatalf("reacquire after release = %+v, err=%v, want granted", second, err)
	}
	if second.Lease.Epoch <= first.Lease.Epoch {
		t.Errorf("epoch after reacquire = %d, want strictly greater than %d", second.Lease.Epoch, first.Lease.Epoch)
	}
}

// TestLeaseReleaseWrongEpochFenced proves Release itself respects the
// fence: a stale epoch is refused, not silently released.
func TestLeaseReleaseWrongEpochFenced(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	if err := m.Release(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch+1); err == nil {
		t.Fatal("Release at a stale epoch = nil error, want ErrLeaseFenced")
	}
	lease, ok, err := m.store.GetLease(ctx, "repo-1", granted.Lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("GetLease after refused release: ok=%v err=%v", ok, err)
	}
	if lease.State != LeaseHeld {
		t.Errorf("lease state after refused release = %v, want unchanged held", lease.State)
	}
}

// TestLeaseNotController asserts a node daemon (isController returning
// false) refuses lease.acquire and lease.release outright (R-21.169).
func TestLeaseNotController(t *testing.T) {
	store := newTestStore(t)
	clock := runtime.NewFixedClock(time.Now())
	node := NewLeaseManager(store, clock, func() bool { return false }, DefaultLeaseDefaults(), nil)
	ctx := context.Background()

	if _, err := node.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a"); err == nil {
		t.Fatal("node daemon Acquire = nil error, want ErrNotController")
	} else if err != ErrNotController {
		t.Errorf("node daemon Acquire error = %v, want ErrNotController", err)
	}
	if err := node.Release(ctx, "repo-1", "internal/jobs/**", 1); err != ErrNotController {
		t.Errorf("node daemon Release error = %v, want ErrNotController", err)
	}
}

// TestLeaseReleasedOnStall covers R-21.193's stall-release co-ownership:
// a current-epoch release (as the AH/S-70.T3 stall transition would
// call) succeeds, moves the lease to released, and wakes the queued
// contender's Acquire; a stale-epoch release is refused with
// ErrLeaseFenced and releases nothing.
func TestLeaseReleasedOnStall(t *testing.T) {
	m, _, _ := newTestLeaseManager(t)
	ctx := context.Background()

	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-stalled")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	queued, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-queued")
	if err != nil || queued.Granted {
		t.Fatalf("expected job-queued to be contended, got %+v, err=%v", queued, err)
	}

	// Stale-epoch stall release: refused, nothing released.
	if err := m.Release(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch+1); err == nil {
		t.Fatal("stale-epoch stall release = nil error, want ErrLeaseFenced")
	}
	still, ok, err := m.store.GetLease(ctx, "repo-1", granted.Lease.ScopeGlob)
	if err != nil || !ok || still.State != LeaseHeld {
		t.Fatalf("lease after refused stall release: state=%v ok=%v err=%v, want still held", still.State, ok, err)
	}

	// Current-epoch stall release: succeeds, wakes the queued contender.
	if err := m.Release(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch); err != nil {
		t.Fatalf("current-epoch stall release: %v", err)
	}
	resumed, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-queued")
	if err != nil || !resumed.Granted {
		t.Fatalf("queued contender after stall release = %+v, err=%v, want granted", resumed, err)
	}
}

// TestWireLeaseReleaseInstallsHook proves WireLeaseRelease actually wires
// the release-on-terminal path end to end: a job that acquires a lease
// and then transitions to a terminal state has that lease released, and
// its queued contender proceeds -- exercising store_job.go's onTerminal
// hook through the real composition-root seam, not just Release called
// directly.
func TestWireLeaseReleaseInstallsHook(t *testing.T) {
	m, _, store := newTestLeaseManager(t)
	ctx := context.Background()
	WireLeaseRelease(store, m)

	if err := store.PutJob(ctx, baseJob("job-a")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	queued, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-b")
	if err != nil || queued.Granted {
		t.Fatalf("expected job-b to be contended, got %+v, err=%v", queued, err)
	}

	if err := store.PutTransition(ctx, "job-a", JobStateFailed, 2); err != nil {
		t.Fatalf("PutTransition to a terminal state: %v", err)
	}

	lease, ok, err := store.GetLease(ctx, "repo-1", granted.Lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("GetLease after terminal transition: ok=%v err=%v", ok, err)
	}
	if lease.State != LeaseReleased {
		t.Fatalf("lease state after terminal transition = %v, want released (WireLeaseRelease not firing)", lease.State)
	}

	resumed, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-b")
	if err != nil || !resumed.Granted {
		t.Fatalf("queued contender after release-on-terminal = %+v, err=%v, want granted", resumed, err)
	}
}
