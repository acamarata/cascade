// Purpose: Scheduler lifecycle tests — skip-missed scheduling (task 6's
//   required TestSchedulerSkipMissed), the Overrun-SKIP policy, and basic
//   Activate/Tick/Close plumbing. Shared test helpers used by every
//   sibling test file in this package (scheduler_lock_test.go,
//   scheduler_orphan_test.go, cron_test.go, retention_register_test.go —
//   all R-14.117-authorized in-package splits of this one file, kept
//   under Art.10.3's 300-line cap the same way bus_test.go's siblings
//   are) live here.
// Constraints: Art.7.1 — every test uses t.TempDir() indirectly via
//   storetest.NewMemStore() (no disk I/O at all) and testkit.FrozenClock
//   (no wall clock, no sleeps — R-14.136).
// SPORT: internal.events.scheduler.Scheduler/ADDED (tests)
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

const testNamespace = "queue"

// testEpoch is the fixed starting instant every test's FrozenClock uses.
var testEpoch = time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)

// newTestScheduler returns a ready-to-Activate Scheduler over a fresh
// MemStore and a FrozenClock started at testEpoch, plus the clock and bus
// so a test can advance time and inspect published events. leaseTTL is
// generous (1000h) by default so a test focused on schedule/fire behavior
// (rather than lock-renewal timing, which scheduler_lock_test.go covers
// directly) never trips an incidental lease expiry; use
// newTestSchedulerTTL for a test that specifically needs a short lease.
func newTestScheduler(t *testing.T, ownerID string) (*Scheduler, *testkit.FrozenClock, *events.Bus, *storetest.MemStore) {
	t.Helper()
	return newTestSchedulerTTL(t, ownerID, 1000*time.Hour)
}

// newTestSchedulerTTL is newTestScheduler with an explicit advisory-lock
// leaseTTL.
func newTestSchedulerTTL(t *testing.T, ownerID string, leaseTTL time.Duration) (*Scheduler, *testkit.FrozenClock, *events.Bus, *storetest.MemStore) {
	t.Helper()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	sched := New(store, testNamespace, clock, bus, ownerID, leaseTTL)
	return sched, clock, bus, store
}

// countingRunnable returns a Runnable that increments *n on every call and
// never errors.
func countingRunnable(n *int) Runnable {
	return func(context.Context) error {
		*n++
		return nil
	}
}

// TestSchedulerSkipMissed is the T-4 AC artifact (task 6): a job whose
// persisted LastFire is far in the past (simulating a long daemon outage)
// fires ZERO times for every missed window and exactly ONCE for the next
// valid window after Activate — never a catch-up burst.
func TestSchedulerSkipMissed(t *testing.T) {
	sched, clock, _, store := newTestScheduler(t, "owner-a")
	ctx := context.Background()
	var fireCount int
	if err := sched.RegisterRunnable("job-owner", countingRunnable(&fireCount)); err != nil {
		t.Fatalf("RegisterRunnable: %v", err)
	}

	// Seed a job directly (bypassing ScheduleJob's zero-LastFire default)
	// with a LastFire 100 windows in the past, simulating state left
	// behind by a daemon that has been down a long time.
	stale := CronJob{ID: "j1", Spec: "@every 1h", Owner: "job-owner", LastFire: testEpoch.Add(-100 * time.Hour)}
	if err := putJob(ctx, store, testNamespace, stale); err != nil {
		t.Fatalf("seed putJob: %v", err)
	}

	report, err := sched.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(report.Scheduled) != 1 || len(report.Orphaned) != 0 {
		t.Fatalf("Activate report = %+v, want exactly one scheduled job", report)
	}

	// Missed windows: advancing by less than the interval and ticking
	// must never fire — regardless of the 100-window backlog.
	for i := 0; i < 5; i++ {
		clock.Advance(10 * time.Minute)
		if _, err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick during missed-window phase: %v", err)
		}
	}
	if fireCount != 0 {
		t.Fatalf("fireCount = %d after missed windows, want 0 (skip-missed)", fireCount)
	}

	// The next VALID window (Activate-time now + 1h) arrives: exactly one
	// firing, never the 100 that a naive LastFire+interval loop would
	// have queued.
	clock.Advance(10 * time.Minute) // now at testEpoch + 1h
	if _, err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick at valid window: %v", err)
	}
	if fireCount != 1 {
		t.Fatalf("fireCount = %d at the next valid window, want exactly 1", fireCount)
	}

	// Steady state: a long subsequent gap ALSO produces exactly one
	// firing, proving skip-missed applies continuously, not only at
	// Activate.
	clock.Advance(500 * time.Hour)
	if _, err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick after long steady-state gap: %v", err)
	}
	if fireCount != 2 {
		t.Fatalf("fireCount = %d after a 500h gap, want exactly 2 (one more, not a burst)", fireCount)
	}
}

// TestSchedulerOverrunSkip proves the Overrun policy: a Tick call that
// arrives while a previous Tick call on the same Scheduler is still
// running (a slow job) returns immediately with Skipped=true and fires
// nothing, and the running job is never invoked twice concurrently.
func TestSchedulerOverrunSkip(t *testing.T) {
	sched, clock, _, _ := newTestScheduler(t, "owner-a")
	ctx := context.Background()

	entered := make(chan struct{})
	release := make(chan struct{})
	var invocations int
	if err := sched.RegisterRunnable("slow", func(context.Context) error {
		invocations++
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("RegisterRunnable: %v", err)
	}
	if err := sched.ScheduleJob(ctx, "slow-job", "@every 1h", "slow"); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if _, err := sched.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	clock.Advance(time.Hour)

	firstDone := make(chan *TickReport)
	go func() {
		r, _ := sched.Tick(ctx)
		firstDone <- r
	}()
	<-entered // the slow job is now blocked inside its Runnable

	second, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("overlapping Tick returned error: %v", err)
	}
	if !second.Skipped {
		t.Fatalf("overlapping Tick.Skipped = false, want true")
	}

	close(release)
	first := <-firstDone
	if first == nil || first.Skipped {
		t.Fatalf("first Tick report = %+v, want a non-skipped completed tick", first)
	}
	if invocations != 1 {
		t.Fatalf("slow Runnable invoked %d times, want exactly 1 (no concurrent/duplicate fire)", invocations)
	}
}

// TestSchedulerTickBeforeActivate proves Tick refuses to run against a
// Scheduler that never successfully Activated.
func TestSchedulerTickBeforeActivate(t *testing.T) {
	sched, _, _, _ := newTestScheduler(t, "owner-a")
	_, err := sched.Tick(context.Background())
	if !errors.Is(err, ErrNotActivated) {
		t.Fatalf("Tick before Activate error = %v, want ErrNotActivated", err)
	}
}

// TestSchedulerScheduleJob_RejectsBadSpec proves ScheduleJob validates the
// spec eagerly, before anything is persisted.
func TestSchedulerScheduleJob_RejectsBadSpec(t *testing.T) {
	sched, _, _, store := newTestScheduler(t, "owner-a")
	ctx := context.Background()
	err := sched.ScheduleJob(ctx, "bad", "not a valid spec", "owner")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ScheduleJob(bad spec) error = %v, want KindInvalidInput", err)
	}
	jobs, lerr := listJobs(ctx, store, testNamespace)
	if lerr != nil {
		t.Fatalf("listJobs: %v", lerr)
	}
	if len(jobs) != 0 {
		t.Fatalf("listJobs after rejected ScheduleJob = %v, want none persisted", jobs)
	}
}

// TestSchedulerScheduleJob_PreservesLastFire proves re-scheduling an
// existing job id preserves its LastFire rather than resetting it.
func TestSchedulerScheduleJob_PreservesLastFire(t *testing.T) {
	sched, _, _, store := newTestScheduler(t, "owner-a")
	ctx := context.Background()
	fired := testEpoch.Add(-time.Hour)
	if err := putJob(ctx, store, testNamespace, CronJob{ID: "j1", Spec: "@every 1h", Owner: "o", LastFire: fired}); err != nil {
		t.Fatalf("seed putJob: %v", err)
	}
	if err := sched.ScheduleJob(ctx, "j1", "@every 2h", "o"); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	got, err := getJob(ctx, store, testNamespace, "j1")
	if err != nil {
		t.Fatalf("getJob: %v", err)
	}
	if !got.LastFire.Equal(fired) {
		t.Fatalf("LastFire = %v after re-schedule, want preserved %v", got.LastFire, fired)
	}
	if got.Spec != "@every 2h" {
		t.Fatalf("Spec = %q after re-schedule, want updated value", got.Spec)
	}
}
