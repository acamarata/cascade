// Purpose: the advisory lock's exclusivity and release-semantics tests
//   (task 6's required TestSchedulerAdvisoryLockExclusion, plus the
//   panic/cancel/process-death release proofs). R-14.117-authorized split
//   of scheduler_test.go.
// SPORT: internal.events.scheduler.advisoryLock/ADDED (tests)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSchedulerAdvisoryLockExclusion is the T-4 AC artifact (task 7): two
// Scheduler instances sharing one Store (the "two store handles on the
// same domain" the contract names) — the second Activate fails with a
// typed cascade.KindConflict error, and the first holder is completely
// unaffected: it stays Active and fires its one registered job exactly
// once, proving "two daemons must not run the same job concurrently" at
// the root (the second daemon never even starts its loop).
func TestSchedulerAdvisoryLockExclusion(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	schedA := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	schedB := New(store, testNamespace, clock, bus, "owner-b", time.Hour)
	ctx := context.Background()

	var fireCount int
	if err := schedA.RegisterRunnable("job-owner", countingRunnable(&fireCount)); err != nil {
		t.Fatalf("RegisterRunnable(A): %v", err)
	}
	if err := schedA.ScheduleJob(ctx, "j1", "@every 30m", "job-owner"); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	if _, err := schedA.Activate(ctx); err != nil {
		t.Fatalf("schedA.Activate: %v", err)
	}
	if !schedA.Active() {
		t.Fatalf("schedA.Active() = false after successful Activate")
	}

	_, errB := schedB.Activate(ctx)
	if !cascade.HasKind(errB, cascade.KindConflict) {
		t.Fatalf("schedB.Activate error = %v, want KindConflict", errB)
	}
	if schedB.Active() {
		t.Fatalf("schedB.Active() = true despite a failed Activate")
	}
	// The first holder is unaffected by the second's failed attempt.
	if !schedA.Active() {
		t.Fatalf("schedA.Active() = false after schedB's failed Activate — first holder was affected")
	}

	clock.Advance(30 * time.Minute)
	if _, err := schedA.Tick(ctx); err != nil {
		t.Fatalf("schedA.Tick: %v", err)
	}
	if fireCount != 1 {
		t.Fatalf("fireCount = %d, want exactly 1 (only the lock holder ever fires)", fireCount)
	}

	// schedB never Activated, so its own Tick refuses to run at all —
	// double protection beyond Activate already having failed.
	if _, err := schedB.Tick(ctx); !errors.Is(err, ErrNotActivated) {
		t.Fatalf("schedB.Tick error = %v, want ErrNotActivated", err)
	}
}

// TestSchedulerLockRelease_OnCancel proves a canceled Activate context
// releases the advisory lock (Close's graceful-shutdown path), letting a
// second daemon acquire it — deterministically, via <-Done(), never a
// sleep or poll (R-14.136).
func TestSchedulerLockRelease_OnCancel(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	schedA := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := schedA.Activate(ctx); err != nil {
		t.Fatalf("schedA.Activate: %v", err)
	}
	cancel()
	<-schedA.Done()

	if schedA.Active() {
		t.Fatalf("schedA.Active() = true after its Activate context was canceled")
	}

	schedB := New(store, testNamespace, clock, bus, "owner-b", time.Hour)
	if _, err := schedB.Activate(context.Background()); err != nil {
		t.Fatalf("schedB.Activate after cancellation = %v, want success (lock released)", err)
	}
}

// TestSchedulerLockRelease_OnJobPanic proves a panicking Runnable is fatal
// to the FIRING scheduler's exclusivity: Tick recovers the panic (the
// test process itself never crashes), the scheduler deactivates, and the
// advisory lock is released — letting a second daemon take over — rather
// than leaving a stuck lock that would mean no daemon ever runs that job
// again.
func TestSchedulerLockRelease_OnJobPanic(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	schedA := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	ctx := context.Background()

	if err := schedA.RegisterRunnable("boom", func(context.Context) error {
		panic("simulated job bug")
	}); err != nil {
		t.Fatalf("RegisterRunnable: %v", err)
	}
	if err := schedA.ScheduleJob(ctx, "j1", "@every 1h", "boom"); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if _, err := schedA.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	clock.Advance(time.Hour)

	_, err := schedA.Tick(ctx)
	if err == nil || !isPanic(err) {
		t.Fatalf("Tick error = %v, want a recovered panic error", err)
	}
	if schedA.Active() {
		t.Fatalf("schedA.Active() = true after a job panic, want the scheduler deactivated")
	}

	schedB := New(store, testNamespace, clock, bus, "owner-b", time.Hour)
	if _, err := schedB.Activate(context.Background()); err != nil {
		t.Fatalf("schedB.Activate after schedA's job panic = %v, want success (lock released)", err)
	}
}

// TestSchedulerLockRelease_OnProcessDeath proves a lease is recovered by
// expiry alone when nothing ever runs a graceful release — the only
// mechanism available for an actual process death (SIGKILL, OOM: no
// defers, no goroutines, nothing executes). schedA acquires the lock and
// is then abandoned (never Ticked, never Closed, its context never
// canceled) — simulating a daemon that dies immediately after Activate.
// Advancing the clock past the lease TTL and Activating a second
// scheduler proves the lock does not survive a crash forever.
func TestSchedulerLockRelease_OnProcessDeath(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	schedA := New(store, testNamespace, clock, bus, "owner-a", time.Hour)

	if _, err := schedA.Activate(context.Background()); err != nil {
		t.Fatalf("schedA.Activate: %v", err)
	}
	// schedA is now abandoned: no Tick, no Close, no cancellation.

	schedB := New(store, testNamespace, clock, bus, "owner-b", time.Hour)
	if _, err := schedB.Activate(context.Background()); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("schedB.Activate before lease expiry = %v, want KindConflict (lease still live)", err)
	}

	clock.Advance(time.Hour + time.Second) // past schedA's un-renewed lease
	if _, err := schedB.Activate(context.Background()); err != nil {
		t.Fatalf("schedB.Activate after lease expiry = %v, want success (a lock that survives a crash means the job never runs again)", err)
	}
}
