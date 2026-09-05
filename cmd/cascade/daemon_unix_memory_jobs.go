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
	"github.com/acamarata/cascade/internal/memory/forget"
	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/internal/runtime"
)

// The CronJob owner labels the two memory jobs register under.
// ListScheduledJobs and OrphanedJobs report them by these exact names.
const (
	memoryConsolidateOwner = "memory-consolidate"
	memoryStalenessOwner   = "memory-staleness"
	// memoryReviewDigestOwner is the weekly review digest (G/S-14.T3).
	memoryReviewDigestOwner = "memory-review-digest"
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
	ledger := memory.NewFileCandidateLedger(base, store, clock, sink)
	queue := review.NewQueue(ledger, clock, sink)
	if err := registerReviewDigestJob(ctx, s, queue, jobCfg, sink, logger); err != nil {
		return nil, err
	}
	return memory.NewAdminHandler(consolidator, jobCfg.Consolidation), nil
}

// registerReviewDigestJob registers the weekly review digest (G/S-14.T3).
//
// The job READS. It builds the digest for the window ending now and
// publishes it; it promotes nothing, retires nothing and writes nothing at
// all, so a daemon that has been running for a year has changed no
// candidate by having fired this fifty-two times.
func registerReviewDigestJob(
	ctx context.Context, s *scheduler.Scheduler, queue *review.Queue,
	jobCfg memory.JobConfig, sink memory.DigestEventSink, logger *slog.Logger,
) error {
	job := review.NewDigestJob(queue, jobCfg.ReviewDigestCadence, sink)
	if err := s.RegisterRunnable(memoryReviewDigestOwner, func(runCtx context.Context) error {
		digest, runErr := job.Run(runCtx)
		if runErr != nil {
			return runErr
		}
		logger.Debug("memory review digest fired", "pending", len(digest.Pending),
			"promoted", len(digest.Promoted), "unreadable", len(digest.Unreadable))
		return nil
	}); err != nil {
		return err
	}
	return s.ScheduleJob(ctx, memoryReviewDigestOwner,
		jobCfg.ReviewDigestSchedule, memoryReviewDigestOwner)
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

// memoryForgetOption builds the forget pipeline this daemon serves
// memory.forget with, as a Handler option the composition root applies in
// one line.
//
// No index is attached. This daemon runs no projection job, so there is no
// index to scrub; attaching a job nothing fills would let the verb report
// a clean index it never had. The verb's own output says the index leg was
// not configured, which is the honest answer until the projection is
// scheduled here.
func memoryForgetOption(
	base string, store memory.MemoryStore, clock runtime.Clock, sink memory.ForgetEventSink,
) memory.HandlerOption {
	return memory.WithForgetPipeline(forget.NewPipeline(base, store, clock, sink))
}

// Compile-time proof that the adapter still satisfies both sinks, so a
// drifting interface fails the build here rather than at the call site.
var (
	_ memory.ConsolidationEventSink = memoryJobEventSink{}
	_ memory.StalenessEventSink     = memoryJobEventSink{}
	_ memory.CandidateEventSink     = memoryJobEventSink{}
	_ memory.DigestEventSink        = memoryJobEventSink{}
	_ memory.ForgetEventSink        = memoryJobEventSink{}
	_ review.ActionEventSink        = memoryJobEventSink{}
)

// CandidatePromoted publishes a promotion by the mechanical lane or by an
// approved review.
func (s memoryJobEventSink) CandidatePromoted(ctx context.Context, ev memory.PromotionEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// CandidateReverted publishes a promotion taken back.
func (s memoryJobEventSink) CandidateReverted(ctx context.Context, ev memory.RevertEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// ReviewActed publishes one human review action, a skip included: an audit
// trail that recorded only the actions that changed something could not
// answer "did anyone look at this".
func (s memoryJobEventSink) ReviewActed(ctx context.Context, ev review.ActionEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// MemoryWeeklyDigestReady publishes the weekly review digest.
//
// Publishing is the whole of the delivery this build performs. The digest
// is put on the local event bus and nothing else: no mail is sent, no
// webhook is called, and no bridge is notified. Anything outbound is a
// subscriber's decision, not this job's.
func (s memoryJobEventSink) MemoryWeeklyDigestReady(ctx context.Context, ev memory.MemoryWeeklyDigest) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// MemoryConsolidated publishes the consolidation event.
func (s memoryJobEventSink) MemoryConsolidated(ctx context.Context, ev memory.ConsolidatedEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// MemoryStaleQueued publishes the staleness event.
func (s memoryJobEventSink) MemoryStaleQueued(ctx context.Context, ev memory.StaleQueuedEvent) error {
	return s.publish(ctx, ev.EventName(), ev)
}

// MemoryForgotten publishes the retirement of one record.
//
// This is the note the backup and sync lane reads to keep a forgotten
// record out of an incremental export, which is why the forget pipeline
// treats a failure here as worth reporting even though the record is
// already gone by then. The payload carries the address, the instant and
// the caller's reason, and no part of the record's text.
func (s memoryJobEventSink) MemoryForgotten(ctx context.Context, ev memory.ForgottenEvent) error {
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
