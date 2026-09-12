//go:build !windows

// Purpose: end-to-end proof that startScheduler (daemon_unix_scheduler.go)
// really registers a configured target's S-42.T4 verification job with a
// Scheduler that is actually Activated and running -- not merely that
// backup.RegisterConfiguredVerificationJobs compiles and passes its own
// package's unit tests.
// SPORT: cmd/cascade/daemon (CHANGED — P1-E19-W4-S42-T4 wiring verification).
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestStartScheduler_VerificationJobPresentInRunningScheduler seeds one
// configured fs target + policy directly into the store startScheduler
// will read, drives the real startScheduler, and asserts the target's
// "backup:verify:<name>" owner is present in the running scheduler's own
// ListScheduledJobs view -- the same real-wiring shape
// TestStartScheduler_RetentionJobsPresentInRunningScheduler already proves
// for retention.
func TestStartScheduler_VerificationJobPresentInRunningScheduler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	paths, store, rawDB, clock := seedWiredVerificationTarget(ctx, t)

	bus := events.New(store, clock)
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	policyWiring, err := wirePolicy(ctx, store, clock, nil)
	if err != nil {
		t.Fatalf("wirePolicy: %v", err)
	}
	sched, _, cleanup, err := startScheduler(ctx, store, rawDB, paths, nil, clock, bus, logger, policyWiring.Router)
	if err != nil {
		t.Fatalf("startScheduler: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		cleanup(context.Background())
	})

	assertVerifyOwnerScheduled(t, sched, "backup:verify:wired-primary")
}

// seedWiredVerificationTarget opens a real runtime store and persists one
// configured fs target + policy under it, returning everything
// startScheduler needs. Split out of the test above under Art.10.3's
// 50-line function cap (funlen counts test functions too).
func seedWiredVerificationTarget(ctx context.Context, t *testing.T) (runtime.PathProvider, provider.Store, *sql.DB, *testkit.FrozenClock) {
	t.Helper()
	paths := fakeDaemonPaths{root: t.TempDir()}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	store, rawDB, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	t.Cleanup(closeStore)

	record := backup.TargetRecord{Name: "wired-primary", Kind: backup.TargetKindFS, FSRoot: t.TempDir()}
	if err := backup.PutTarget(ctx, store, schedulerNamespace, record); err != nil {
		t.Fatalf("PutTarget: %v", err)
	}
	pol := backup.TargetPolicy{Target: "wired-primary", CronSpec: "0 3 * * *", Domains: []string{"config"}}
	if err := backup.PutPolicy(ctx, store, schedulerNamespace, pol); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	return paths, store, rawDB, clock
}

// assertVerifyOwnerScheduled checks wantOwner is present in sched's own
// ListScheduledJobs view.
func assertVerifyOwnerScheduled(t *testing.T, sched *scheduler.Scheduler, wantOwner string) {
	t.Helper()
	jobs, err := sched.ListScheduledJobs()
	if err != nil {
		t.Fatalf("ListScheduledJobs: %v", err)
	}
	for _, j := range jobs {
		if j.Owner == wantOwner {
			return
		}
	}
	t.Fatalf("owner %q not present in the running scheduler's ListScheduledJobs (%d jobs total) -- "+
		"RegisterConfiguredVerificationJobs is not reaching the real Activated scheduler", wantOwner, len(jobs))
}
