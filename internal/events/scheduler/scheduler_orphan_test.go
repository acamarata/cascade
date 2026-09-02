// Purpose: orphaned-owner detection tests (task 6's required
//   TestSchedulerOrphanedOwnerSurfaces, plus the corrupt-spec variant).
//   R-14.117-authorized split of scheduler_test.go.
// SPORT: internal.events.scheduler.Scheduler/ADDED (Activate orphan
//   detection, tests) (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestSchedulerOrphanedOwnerSurfaces is the T-4 AC artifact (task 8):
// register a job, restart the scheduler WITHOUT the owner's Runnable, and
// assert the ERROR-level event is emitted and the job is reported — never
// silently skipped.
func TestSchedulerOrphanedOwnerSurfaces(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	ctx := context.Background()

	// "Register a job": a first scheduler instance, WITH the owner's
	// Runnable registered, schedules it — this is what persists the
	// CronJob record across the simulated restart.
	first := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	if err := first.RegisterRunnable("plugin-x", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("RegisterRunnable: %v", err)
	}
	if err := first.ScheduleJob(ctx, "j1", "@every 1h", "plugin-x"); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if _, err := first.Activate(ctx); err != nil {
		t.Fatalf("first.Activate: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("first.Close: %v", err)
	}

	// "Restart the scheduler without the owner runnable": a fresh
	// Scheduler instance over the SAME store, simulating a daemon
	// restart after the "plugin-x" plugin was removed — nobody calls
	// RegisterRunnable("plugin-x", ...) this time.
	second := New(store, testNamespace, clock, bus, "owner-b", time.Hour)
	report, err := second.Activate(ctx)
	if err != nil {
		t.Fatalf("second.Activate: %v", err)
	}

	if len(report.Orphaned) != 1 || report.Orphaned[0].ID != "j1" {
		t.Fatalf("ActivateReport.Orphaned = %+v, want exactly job j1", report.Orphaned)
	}
	if len(report.Scheduled) != 0 {
		t.Fatalf("ActivateReport.Scheduled = %+v, want none (j1's owner is unregistered)", report.Scheduled)
	}

	assertOrphanedJobSurfaces(ctx, t, second, bus)
}

// assertOrphanedJobSurfaces asserts j1's orphan is queryable via
// OrphanedJobs and ListScheduledJobs (never deleted), and that an
// ERROR-level EventKindSchedulerOrphanedOwner event was published for it —
// split out of TestSchedulerOrphanedOwnerSurfaces under Art.10.3's
// 50-line function cap.
func assertOrphanedJobSurfaces(ctx context.Context, t *testing.T, sched *Scheduler, bus *events.Bus) {
	t.Helper()
	orphaned := sched.OrphanedJobs()
	if len(orphaned) != 1 || orphaned[0].ID != "j1" || orphaned[0].Owner != "plugin-x" {
		t.Fatalf("OrphanedJobs() = %+v, want [j1/plugin-x] (never silently skipped)", orphaned)
	}

	all, lerr := sched.ListScheduledJobs()
	if lerr != nil {
		t.Fatalf("ListScheduledJobs: %v", lerr)
	}
	if len(all) != 1 || all[0].ID != "j1" {
		t.Fatalf("ListScheduledJobs() = %+v, want the orphaned job still persisted", all)
	}

	replayed, rerr := bus.Replay(ctx, testNamespace, 0)
	if rerr != nil {
		t.Fatalf("bus.Replay: %v", rerr)
	}
	found := false
	for _, ev := range replayed {
		if ev.Kind != EventKindSchedulerOrphanedOwner {
			continue
		}
		var payload schedulerEventPayload
		if jerr := json.Unmarshal(ev.Payload, &payload); jerr != nil {
			t.Fatalf("unmarshal orphaned-owner event payload: %v", jerr)
		}
		if payload.Level == "error" && payload.JobID == "j1" && payload.Owner == "plugin-x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no EventKindSchedulerOrphanedOwner error-level event found for j1/plugin-x in %+v", replayed)
	}
}

// TestSchedulerOrphanedOwner_CorruptSpec proves a persisted job whose Spec
// no longer parses is ALSO treated as orphaned (reported, not silently
// dropped) rather than failing Activate outright for every other job.
func TestSchedulerOrphanedOwner_CorruptSpec(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	ctx := context.Background()

	sched := New(store, testNamespace, clock, bus, "owner-a", time.Hour)
	if err := sched.RegisterRunnable("owner-x", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("RegisterRunnable: %v", err)
	}
	// Seed directly to bypass ScheduleJob's own up-front validation —
	// simulating a record that became invalid after it was written (e.g.
	// a future grammar change).
	if err := putJob(ctx, store, testNamespace, CronJob{ID: "bad", Spec: "not a valid spec", Owner: "owner-x"}); err != nil {
		t.Fatalf("seed putJob: %v", err)
	}
	if err := sched.ScheduleJob(ctx, "good", "@every 1h", "owner-x"); err != nil {
		t.Fatalf("ScheduleJob(good): %v", err)
	}

	report, err := sched.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(report.Scheduled) != 1 || report.Scheduled[0].ID != "good" {
		t.Fatalf("report.Scheduled = %+v, want exactly [good]", report.Scheduled)
	}
	if len(report.Orphaned) != 1 || report.Orphaned[0].ID != "bad" {
		t.Fatalf("report.Orphaned = %+v, want exactly [bad]", report.Orphaned)
	}
}
