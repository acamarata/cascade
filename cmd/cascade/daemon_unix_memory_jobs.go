//go:build !windows

// Purpose: the daemon's memory-maintenance wiring — it builds the real
//
//	Consolidator and StalenessScanner over the SAME store tree
//	registerMemoryHandler serves, registers both as runnables on the
//	running cron scheduler, and publishes their events on the real bus.
//	Without this file both jobs would exist, be tested, and never fire in
//	a shipped daemon, which is exactly the defect R-14.175 named.
//
// Inputs: the constructed (not yet Activated) *scheduler.Scheduler, the
//
//	path provider that resolves the memory store root, the loaded
//	*runtime.Config the [memory] section is read from, the process Clock,
//	the *events.Bus, and a logger.
//
// Outputs: two persisted CronJob records ("memory-consolidate",
//
//	"memory-staleness") and their registered Runnables, plus the
//	AdminHandler the memory.consolidate verb is served by.
//
// Constraints: no bare time.Now (the jobs take the injected clock); a
//
//	job's own failure is returned to the scheduler, which logs it, never
//	swallowed; the runnables are constructed exactly as this file
//	constructs them, so the production-path test fires the real thing.
//
// SPORT: cmd/cascade/daemon (CHANGED — P1-E07-W2-S13-T4 memory job wiring).
package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
)

// The CronJob owner labels the two memory jobs register under.
// ListScheduledJobs and OrphanedJobs report them by these exact names.
const (
	memoryConsolidateOwner = "memory-consolidate"
	memoryStalenessOwner   = "memory-staleness"
)

// memoryJobsEventNamespace is the bus namespace the two jobs publish
// under. It is the same namespace the SOUL divergence event uses, so
// everything the memory subsystem says about itself arrives on one log.
const memoryJobsEventNamespace = "memory"

// memoryJobsEventSource identifies the publisher of both events.
const memoryJobsEventSource = "internal/memory"

// registerMemoryJobs registers the consolidation job and the staleness
// scan on s, at the schedules the [memory] config section resolves to,
// and returns the AdminHandler that serves the manual memory.consolidate
// trigger over the SAME Consolidator — so a manual run and a scheduled
// run share the one re-entrancy guard rather than racing each other.
//
// Call it BEFORE Activate, so the two jobs are scheduled rather than
// reported as orphaned.
//
// # Exclusion between daemons
//
// Neither job takes a lock of its own. The scheduler's advisory lock
// (C/S-04.T4) is the exclusion: a second daemon over the same store fails
// to acquire the lease, never Activates, and therefore never ticks these
// runnables at all. Per §D-3 a shared store across daemons is unsupported
// anyway; adding a second lock inside the job would be a parallel
// mechanism that could disagree with the first.
func registerMemoryJobs(
	ctx context.Context, s *scheduler.Scheduler, paths runtime.PathProvider,
	cfg *runtime.Config, clock runtime.Clock, bus *events.Bus, logger *slog.Logger,
) (*memory.AdminHandler, error) {
	jobCfg, err := memoryJobConfig(cfg, logger)
	if err != nil {
		return nil, err
	}
	base := memoryStoreDir(paths)
	store := memory.NewFileStore(base, clock)
	sink := memoryJobEventSink{bus: bus}
	consolidator := memory.NewConsolidator(base, store, clock, sink)
	scanner := memory.NewStalenessScanner(base, store, clock, sink)

	if err := s.RegisterRunnable(memoryConsolidateOwner, func(runCtx context.Context) error {
		report, runErr := consolidator.ConsolidateMemories(runCtx, jobCfg.Consolidation)
		if runErr != nil {
			return runErr
		}
		logger.Debug("memory consolidation ran", "merged", report.Merged,
			"retired", report.Retired, "no_change", report.NoChange, "skipped", report.Skipped)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.ScheduleJob(ctx, memoryConsolidateOwner,
		jobCfg.ConsolidationSchedule, memoryConsolidateOwner); err != nil {
		return nil, err
	}
	if err := registerStalenessJob(ctx, s, scanner, jobCfg, logger); err != nil {
		return nil, err
	}
	return memory.NewAdminHandler(consolidator, jobCfg.Consolidation), nil
}

// registerStalenessJob is the staleness half of registerMemoryJobs, split
// out under Art.10.3's 50-line function cap.
func registerStalenessJob(
	ctx context.Context, s *scheduler.Scheduler, scanner *memory.StalenessScanner,
	jobCfg memory.JobConfig, logger *slog.Logger,
) error {
	if err := s.RegisterRunnable(memoryStalenessOwner, func(runCtx context.Context) error {
		report, runErr := scanner.ScanStaleness(runCtx, jobCfg.Staleness)
		if runErr != nil {
			return runErr
		}
		logger.Debug("memory staleness scan ran", "queued", report.Queued,
			"dropped", report.Dropped, "total", report.Total, "idempotent", report.Idempotent)
		return nil
	}); err != nil {
		return err
	}
	return s.ScheduleJob(ctx, memoryStalenessOwner, jobCfg.StalenessSchedule, memoryStalenessOwner)
}

// memoryJobConfig resolves the [memory] section, falling back to the
// shipped defaults with a WARNING when the section is malformed.
//
// It warns rather than refusing the daemon start for one reason: this
// section configures two background maintenance jobs, and a typo in a
// staleness window must not stop a user's daemon from serving memory at
// all. The warning is loud, and the defaults it falls back to are the
// documented ones, so nothing runs on a value nobody chose.
func memoryJobConfig(cfg *runtime.Config, logger *slog.Logger) (memory.JobConfig, error) {
	if cfg == nil {
		return memory.DefaultJobConfig(), nil
	}
	jobCfg, err := memory.ParseJobConfig(cfg.Extra["memory"])
	if err != nil {
		logger.Warn("the [memory] config section was refused; "+
			"the memory maintenance jobs are running on their shipped defaults", "error", err)
		return memory.DefaultJobConfig(), nil
	}
	return jobCfg, nil
}

// memoryJobEventSink publishes the two maintenance events on the real
// bus. It is the composition root's adapter for the reason
// soulDivergenceSink is: internal/memory declares the sinks it needs
// structurally and never imports internal/events.
//
// Both payloads carry ADDRESSES and counts, never a record's body. A bus
// event fans out to every subscriber, and a subscriber that only asked to
// know a consolidation happened must not receive the text of the records
// it retired.
type memoryJobEventSink struct {
	bus *events.Bus
}

// Compile-time proof that the adapter still satisfies both sinks, so a
// drifting interface fails the build here rather than at the call site.
var (
	_ memory.ConsolidationEventSink = memoryJobEventSink{}
	_ memory.StalenessEventSink     = memoryJobEventSink{}
)

// MemoryConsolidated publishes the consolidation event.
func (s memoryJobEventSink) MemoryConsolidated(ctx context.Context, ev memory.ConsolidatedEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// MemoryStaleQueued publishes the staleness event.
func (s memoryJobEventSink) MemoryStaleQueued(ctx context.Context, ev memory.StaleQueuedEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// publish marshals payload and publishes it. A nil bus discards the event,
// which is the documented no-bus configuration rather than a nil-pointer
// panic inside a job the daemon otherwise runs fine.
func (s memoryJobEventSink) publish(ctx context.Context, name string, payload any) error {
	if s.bus == nil {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.bus.Publish(ctx, memoryJobsEventNamespace, events.EventKind(name),
		memoryJobsEventSource, body)
	return err
}
