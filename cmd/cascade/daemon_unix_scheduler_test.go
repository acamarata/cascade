//go:build !windows

// Purpose: end-to-end proof that startScheduler — the actual
//
//	production composition-root function daemon_unix.go's
//	platformDaemonRun calls — registers the retention jobs
//	(internal/events/scheduler.RegisterRetentionJobs) with a Scheduler
//	that is actually Activated and running, not merely constructed.
//	Closes R-14.175's "RegisterRetentionJobs is never registered with a
//	running scheduler" gap.
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 scheduler wiring
//
//	verification).
package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestStartScheduler_RetentionJobsPresentInRunningScheduler drives
// startScheduler against a real openRuntimeStore-opened cascade.db and
// asserts both retention jobs (retention-prune, retention-vacuum) are
// present in ListScheduledJobs — the running Scheduler's own persisted
// view — after startScheduler returns, and that the scheduler reports no
// orphaned jobs (which would mean the runnable was registered against a
// different Scheduler instance than the one that Activated).
func TestStartScheduler_RetentionJobsPresentInRunningScheduler(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store, rawDB, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	t.Cleanup(closeStore)

	bus := events.New(store, clock)
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	sched, admin, cleanup, err := startScheduler(
		ctx, store, rawDB, paths, nil, clock, bus, logger)
	if err != nil {
		t.Fatalf("startScheduler: %v", err)
	}
	if admin == nil {
		t.Fatal("startScheduler returned no memory AdminHandler, so memory.consolidate would be unreachable")
	}
	t.Cleanup(func() {
		cancel()
		cleanup(context.Background())
	})

	jobs, err := sched.ListScheduledJobs()
	if err != nil {
		t.Fatalf("ListScheduledJobs: %v", err)
	}
	wantOwners := map[string]bool{
		"retention-prune": false, "retention-vacuum": false,
		memoryConsolidateOwner: false, memoryStalenessOwner: false,
	}
	for _, j := range jobs {
		if _, ok := wantOwners[j.Owner]; ok {
			wantOwners[j.Owner] = true
		}
	}
	for owner, seen := range wantOwners {
		if !seen {
			t.Errorf("scheduled job owner %q not found in the running scheduler's ListScheduledJobs (%d jobs total)", owner, len(jobs))
		}
	}
	if orphaned := sched.OrphanedJobs(); len(orphaned) != 0 {
		t.Errorf("OrphanedJobs = %v, want none — a job registered against a different Scheduler than the one Activated would show up here", orphaned)
	}
}

// testWriter adapts *testing.T to io.Writer for slog output during tests.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
