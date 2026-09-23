//go:build !windows

// Purpose: the memory projection's background composition root
//
//	(P1-E07-W5-S92-T1; R-14.294 item 10; PCI
//	s47t1-memory-projection-never-run-in-production). Nothing in
//	production called ProjectionJob.Run or Rebuild before this file: the
//	only construction was daemon_unix_recall_what.go's buildRecallWhatHandler,
//	which only ever SEARCHES the projection, so recall.what's memory leg
//	searched an index nothing filled. startMemoryProjection is that
//	caller, run once at daemon start and then on memoryProjectionInterval,
//	mirroring startFleetMetricsConsumer's own "log and keep going" shape
//	(daemon_unix_metrics.go) rather than the scheduler's cron-style
//	dispatch -- this is a fixed-interval maintenance loop, not a job a
//	user schedules.
//
// Inputs: the daemon's shared runtime.PathProvider, provider.Store and
//
//	runtime.Clock (wireBackgroundSubsystems already threads all three
//	through to buildRecallWhatHandler's own ProjectionJob), and a
//	*slog.Logger for the WARN line a failed run reports.
//
// Outputs: none directly -- the projection's rows, postings and vectors,
//
//	written through the SAME store handle every other daemon subsystem
//	uses (no second sqlite connection opened for this).
//
// Constraints: the loop never panics the daemon (Art.1): a run failure is
//
//	logged at WARN with the partial ProjectionResult and retried on the
//	next tick, exactly like a stale index rather than a crashed process.
//	No bare time.Now/time.NewTicker in the loop body -- pacing goes
//	through runtime.Ticker (internal/runtime/metrics_emitter.go's own
//	seam, reused rather than re-invented), so a test drives it with a
//	fake tick instead of a real 60s wait (R-14.136).
//
// SPORT: cmd/cascade daemon_unix_memory_projection.go [ADD]; memory
//
//	projection WIRED (P1-E07-W5-S92-T1).
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// memoryProjectionInterval is the background loop's tick interval, after
// its first, immediate run at daemon start (R-14.294: "Interval 60 s plus
// a start run; no bus-event trigger in P1").
const memoryProjectionInterval = 60 * time.Second

// memoryProjectionRunner is the minimal *memory.ProjectionJob surface the
// loop needs -- narrow so a test can inject a fake that fails on demand
// without a real file store or sqlite database.
type memoryProjectionRunner interface {
	Run(ctx context.Context) (memory.ProjectionResult, error)
}

// startMemoryProjection builds the real *memory.ProjectionJob over the
// SAME memory files directory and provider.Store handle
// buildRecallWhatHandler's own memory leg uses (memoryStoreDir, D2's
// established pattern) and starts the background loop in its own
// goroutine tied to ctx. A nil store is a daemon that has not finished
// opening its own database yet; the loop simply never starts rather than
// racing that construction.
func startMemoryProjection(ctx context.Context, paths runtime.PathProvider, store provider.Store, clock runtime.Clock, logger *slog.Logger) {
	if store == nil {
		return
	}
	job := memory.NewProjectionJob(memory.NewFileStore(memoryStoreDir(paths), clock), store, nil, nil, clock)
	go runMemoryProjectionLoop(ctx, job, runtime.NewSystemTicker(memoryProjectionInterval), logger, nil)
}

// runMemoryProjectionLoop is startMemoryProjection's loop body, split out
// so a test can drive it with a fake runtime.Ticker instead of a real
// 60-second wait. It runs one pass immediately, then one per tick, until
// ctx ends -- never panicking the daemon over a run failure.
//
// afterRun, when non-nil, fires after each pass (start run and every
// tick) settles -- the same synchronization hook shape
// internal/fleet.HeadroomPublisher's own afterTick field uses, so a test
// never has to poll or sleep for the loop's goroutine to make progress
// (R-14.136). Production always passes nil.
func runMemoryProjectionLoop(ctx context.Context, job memoryProjectionRunner, ticker runtime.Ticker, logger *slog.Logger, afterRun func()) {
	defer ticker.Stop()
	runMemoryProjectionOnce(ctx, job, logger)
	if afterRun != nil {
		afterRun()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			runMemoryProjectionOnce(ctx, job, logger)
			if afterRun != nil {
				afterRun()
			}
		}
	}
}

// runMemoryProjectionOnce runs one Run pass. A failure is logged at WARN
// with the partial ProjectionResult's counts and retried on the next tick
// -- the index is derived state, so a stalled run means recall.what's
// memory leg searches a stale projection, never a hole in the daemon
// itself (Run already rebuilds on its own on a ProjectionVersion
// mismatch, db_projection.go's own doc comment).
func runMemoryProjectionOnce(ctx context.Context, job memoryProjectionRunner, logger *slog.Logger) {
	res, err := job.Run(ctx)
	if err != nil {
		logger.Warn("memory projection run failed, retrying next tick",
			slog.String("error", err.Error()),
			slog.Int("scanned", res.Scanned), slog.Int("upserted", res.Upserted),
			slog.Int("retired", res.Retired), slog.Int("failed", res.Failed))
		return
	}
	if res.Failed > 0 {
		logger.Warn("memory projection run completed with per-record failures",
			slog.Int("scanned", res.Scanned), slog.Int("upserted", res.Upserted),
			slog.Int("retired", res.Retired), slog.Int("failed", res.Failed))
	}
}
