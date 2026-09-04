//go:build !windows

// Purpose: the daemon run path's retention-scheduler wiring —
//
//	constructs the real scheduler.Scheduler, registers
//	scheduler.RegisterRetentionJobs against it, Activates it, and drives
//	it with scheduler.RunLoop over a real runtime.Ticker. R-14.175 found
//	RegisterRetentionJobs built and tested with no production caller;
//	this file is that caller. Split from daemon_unix.go under R-14.117
//	(Art.10.3's 300-line cap).
//
// Inputs: the real provider.Store and raw *sql.DB openRuntimeStore
//
//	opened (retention's runnables need direct SQL, not the KV Store
//	contract — see daemon_unix_store.go's doc comment), the process's
//	Clock, and the *events.Bus the daemon publishes to.
//
// Outputs: a started *scheduler.Scheduler and a cleanup func the caller
//
//	runs on shutdown (stops the tick loop via context cancellation,
//	elsewhere, then releases the advisory lock via Close).
//
// Constraints: no bare time.Now (Art.7.3); ownerID is generated from
//
//	crypto/rand rather than a new go.mod dependency, satisfying
//	scheduler.New's "distinct across concurrently-running daemon
//	processes" requirement without widening go.mod for this wiring.
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 scheduler wiring).
package main

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/provider"
)

// schedulerNamespace is the provider.Store namespace CronJob records and
// the advisory lock persist under.
const schedulerNamespace = "daemon-scheduler"

// schedulerLeaseTTL is the advisory-lock lease duration this composition
// root supplies (08-INIT-CONFIG-SPEC.md §3 declares no [scheduler]
// config section — see scheduler.New's own doc comment — so this is the
// one explicit value this wiring, not the package, is responsible for
// choosing; five minutes comfortably exceeds schedulerTickInterval so a
// live daemon always renews its lease before it would lapse).
const schedulerLeaseTTL = 5 * time.Minute

// schedulerTickInterval is how often RunLoop calls Tick. Retention's own
// jobs fire at most weekly (retention_register.go's retentionInterval),
// so this only needs to be frequent enough that a due job is not left
// waiting long after its nextFire — it is not itself a retention-policy
// value.
const schedulerTickInterval = time.Minute

// newSchedulerOwnerID generates a per-process identifier distinct across
// concurrently-running daemons, per scheduler.New's doc comment ("e.g. a
// UUID generated once at process start"). 16 bytes of crypto/rand,
// hex-encoded, avoids adding a UUID dependency for a value whose only
// contract is uniqueness, never a parseable UUID shape.
func newSchedulerOwnerID() (string, error) {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// startScheduler builds the real Scheduler over store/rawDB, registers
// the retention jobs (internal/events/scheduler.RegisterRetentionJobs),
// Activates it, and starts RunLoop in a background goroutine tied to
// ctx. The returned cleanup func waits for RunLoop to observe ctx's
// cancellation and then releases the advisory lock via Close — the
// caller is expected to have already cancelled ctx (or its parent)
// before calling cleanup, matching every other platformDaemonRun
// subsystem's shutdown order.
func startScheduler(ctx context.Context, store provider.Store, rawDB *sql.DB, paths runtime.PathProvider, cfg *runtime.Config, clock runtime.Clock, bus *events.Bus, logger *slog.Logger) (*scheduler.Scheduler, *memory.AdminHandler, func(context.Context), error) {
	ownerID, err := newSchedulerOwnerID()
	if err != nil {
		return nil, nil, nil, err
	}
	sched := scheduler.New(store, schedulerNamespace, clock, bus, ownerID, schedulerLeaseTTL)

	if err := scheduler.RegisterRetentionJobs(ctx, sched, rawDB, storage.RetentionConfig{}, clock); err != nil {
		return nil, nil, nil, err
	}
	// The memory maintenance jobs (G/S-13.T4) register on the SAME
	// scheduler, before Activate, so they are scheduled rather than
	// reported as orphaned. The AdminHandler this returns is the one the
	// memory.consolidate verb must be served by: it shares the
	// Consolidator — and therefore the re-entrancy guard — with the
	// scheduled job, so a manual run and a background run can never be in
	// flight over the same tree at once.
	admin, err := registerMemoryJobs(ctx, sched, paths, cfg, clock, bus, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := sched.Activate(ctx); err != nil {
		return nil, nil, nil, err
	}

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		scheduler.RunLoop(ctx, sched, runtime.NewSystemTicker(schedulerTickInterval), func(err error) {
			logger.Error("scheduler tick failed", "error", err)
		})
	}()

	cleanup := func(closeCtx context.Context) {
		<-loopDone
		if err := sched.Close(closeCtx); err != nil {
			logger.Error("scheduler close failed", "error", err)
		}
	}
	return sched, admin, cleanup, nil
}
