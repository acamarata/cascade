// Purpose: Scheduler.Activate, Close, and watchCancellation (tasks 3 and
//   5's Activate-time half: skip-missed nextFire computation and
//   orphaned-owner detection). Split from scheduler.go under R-14.117's
//   authorized-split allowance (Art.10.3's 300-line cap; mechanical
//   relocation — scheduler.go, this file, and scheduler_tick.go together
//   are the one T-4 Scheduler unit).
// Constraints: no bare time.Now (R-14.11) — every timestamp comes from
//   s.clock.
// SPORT: internal.events.scheduler.Scheduler/ADDED (Activate/Close)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"sync"
	"time"
)

// Activate acquires the domain-level advisory lock (lock.go), loads every
// persisted CronJob from Store, computes each one's skip-missed nextFire
// (the next occurrence strictly after clock.Now(), regardless of
// LastFire), and detects orphaned owners. It returns a cascade.KindConflict
// error, unaffecting whichever daemon already holds the lock, if another
// owner's lease is still live — Activate makes NO other state changes in
// that case.
//
// A job whose Owner has no registered Runnable is NOT scheduled to fire;
// it is recorded in the returned report's Orphaned list and in
// OrphanedJobs, and an EventKindSchedulerOrphanedOwner error-level event
// is published for it — never silently dropped (Art.1). A job whose
// persisted Spec no longer parses (a corrupt or since-invalidated record)
// is treated the same way: reported, published, not scheduled, and
// Activate continues with every other job rather than failing outright.
func (s *Scheduler) Activate(ctx context.Context) (*ActivateReport, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if s.lock == nil {
		s.lock = newAdvisoryLock(s.store, s.namespace, s.ownerID, s.leaseTTL, s.clock)
	}
	if err := s.lock.Acquire(ctx); err != nil {
		return nil, err
	}

	persisted, err := listJobs(ctx, s.store, s.namespace)
	if err != nil {
		_ = s.lock.Release(ctx)
		return nil, err
	}

	s.mu.Lock()
	s.runnables = cloneRunnables(s.runnables)
	runnables := s.runnables
	s.mu.Unlock()

	jobs, report := s.scheduleJobs(ctx, persisted, runnables, s.clock.Now())

	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.active = true
	s.jobs = jobs
	s.orphaned = report.Orphaned
	s.done = done
	s.doneOnce = sync.OnceFunc(func() { close(done) })
	s.cancelWait = cancel
	s.mu.Unlock()

	go s.watchCancellation(ctx, waitCtx)

	return report, nil
}

// scheduleJobs computes each persisted job's in-memory scheduledJob (its
// parsed Schedule and skip-missed nextFire) or, for an orphaned owner or
// unparseable spec, records it in the report and publishes an
// EventKindSchedulerOrphanedOwner event instead — split out of Activate
// under Art.10.3's 50-line function cap.
func (s *Scheduler) scheduleJobs(ctx context.Context, persisted []CronJob, runnables map[string]Runnable, now time.Time) (map[string]*scheduledJob, *ActivateReport) {
	jobs := make(map[string]*scheduledJob, len(persisted))
	report := &ActivateReport{}
	for _, job := range persisted {
		if _, ok := runnables[job.Owner]; !ok {
			report.Orphaned = append(report.Orphaned, job)
			s.emitEvent(ctx, EventKindSchedulerOrphanedOwner, job, "owner has no registered runnable")
			continue
		}
		sched, perr := ParseSpec(job.Spec)
		if perr != nil {
			report.Orphaned = append(report.Orphaned, job)
			s.emitEvent(ctx, EventKindSchedulerOrphanedOwner, job, "spec no longer parses: "+perr.Error())
			continue
		}
		next, nerr := sched.NextAfter(now)
		if nerr != nil {
			report.Orphaned = append(report.Orphaned, job)
			s.emitEvent(ctx, EventKindSchedulerOrphanedOwner, job, "spec has no next occurrence: "+nerr.Error())
			continue
		}
		jobs[job.ID] = &scheduledJob{CronJob: job, schedule: sched, nextFire: next}
		report.Scheduled = append(report.Scheduled, job)
	}
	return jobs, report
}

// watchCancellation calls Close once ctx (Activate's caller-supplied
// context) is Done, so a canceled daemon-lifetime context always releases
// the advisory lock without requiring every caller to remember an
// explicit Close. waitCtx is canceled by Close itself, so an explicit
// Close stops this goroutine immediately rather than leaving it parked on
// ctx.Done() forever.
func (s *Scheduler) watchCancellation(ctx, waitCtx context.Context) {
	select {
	case <-ctx.Done():
		_ = s.Close(context.Background())
	case <-waitCtx.Done():
	}
}

// Close releases the advisory lock and marks the scheduler inactive. It is
// idempotent and safe to call whether or not Activate ever succeeded, and
// a Close that finds the scheduler already closing does not return until
// that other Close's Release has actually landed (closeMu) — a caller
// that exits the process on Close's return, as the daemon's shutdown
// does, would otherwise leave the lease written for a concurrent
// watchCancellation to never finish removing.
// Together with a fatal in-Tick error (scheduler_tick.go) and a lease
// simply not being renewed before it expires (lock.go), this is one of
// the three ways a held lease actually goes away: (1) Close — an explicit
// or context-triggered graceful shutdown, always releases immediately;
// (2) a panicking Runnable — Tick recovers it, treats it as fatal to this
// instance's exclusivity, and releases immediately; (3) process death —
// nothing runs, including Close, so the lease is only ever recovered by a
// future Acquire noticing it has expired (lock.go's Acquire).
func (s *Scheduler) Close(ctx context.Context) error {
	// Held for the whole function, Release included, so a second Close
	// cannot report "already closed" until the first has actually
	// released the lease. See Scheduler.closeMu.
	s.closeMu.Lock()
	defer s.closeMu.Unlock()

	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return nil
	}
	s.active = false
	if s.cancelWait != nil {
		s.cancelWait()
	}
	markDone := s.doneOnce
	s.mu.Unlock()

	if markDone != nil {
		markDone()
	}
	if s.lock == nil {
		return nil
	}
	return s.lock.Release(ctx)
}

// cloneRunnables returns a snapshot copy of runnables so Activate's
// orphan-detection scan is never racing a concurrent RegisterRunnable
// call made after Activate started but before it finished.
func cloneRunnables(runnables map[string]Runnable) map[string]Runnable {
	out := make(map[string]Runnable, len(runnables))
	for k, v := range runnables {
		out[k] = v
	}
	return out
}
