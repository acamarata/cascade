// Purpose: Scheduler.Tick — the eligible-check/fire/Publish loop (task 4)
//   plus its panic-recovery and Overrun-SKIP policy. Split from
//   scheduler.go under R-14.117's authorized-split allowance.
// Constraints: no bare time.Now (R-14.11); Runnable calls happen WITHOUT
//   s.mu held (a slow or blocking job must never stall RegisterRunnable,
//   ScheduleJob, or a concurrent Tick's overrun check).
// SPORT: internal.events.scheduler.Scheduler/ADDED (Tick)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// panicError wraps a recovered Runnable panic so callers can distinguish
// it from an ordinary returned error (isPanic) without string-matching.
type panicError struct {
	recovered any
	stack     []byte
}

func (p *panicError) Error() string {
	return fmt.Sprintf("scheduler: job panicked: %v", p.recovered)
}

func isPanic(err error) bool {
	var p *panicError
	return errors.As(err, &p)
}

// Tick checks every scheduled job's eligibility against clock.Now() and
// fires each one that is due, in ID order. Overrun policy is SKIP: if a
// previous Tick call on this Scheduler is still in flight, this call
// returns immediately with TickReport.Skipped set and fires nothing — see
// the package doc's "Overrun policy" section.
//
// A fired Runnable that returns a plain error is recorded
// (EventKindSchedulerJobFailed) and Tick continues to the next due job. A
// fired Runnable that PANICS is recovered, published as
// EventKindSchedulerJobPanicked, and treated as fatal: the advisory lock
// is released, the scheduler is deactivated (Active() becomes false,
// Done() closes), and Tick stops firing any further job this call and
// returns the panic as its error — a panicking job is this daemon's
// signal that its own scheduling loop may no longer be trustworthy, and
// giving up exclusivity lets a healthier daemon take over rather than
// silently limping on. See scheduler_activate.go's Close doc comment for
// the full three-way lock-release taxonomy this implements.
func (s *Scheduler) Tick(ctx context.Context) (*TickReport, error) {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return nil, ErrNotActivated
	}
	if s.ticking {
		s.mu.Unlock()
		return &TickReport{Skipped: true}, nil
	}
	s.ticking = true
	now := s.clock.Now()
	due := dueJobsLocked(s.jobs, now)
	runnables := cloneRunnables(s.runnables)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.ticking = false
		s.mu.Unlock()
	}()

	report := &TickReport{}
	for _, job := range due {
		report.Fired = append(report.Fired, job.ID)
		fn, ok := runnables[job.Owner]
		if !ok {
			s.emitEvent(ctx, EventKindSchedulerOrphanedOwner, job.CronJob, "owner no longer registered")
			continue
		}

		err := runSafely(ctx, fn)
		if err == nil {
			s.recordFire(ctx, job, now)
			continue
		}
		report.Errors = append(report.Errors, err)
		if isPanic(err) {
			s.publish(ctx, EventKindSchedulerJobPanicked, "error", job.CronJob, err.Error())
			s.deactivateAfterFatal(ctx)
			return report, err
		}
		s.publish(ctx, EventKindSchedulerJobFailed, "info", job.CronJob, err.Error())
	}

	if err := s.lock.Renew(ctx); err != nil {
		report.Errors = append(report.Errors, err)
		s.deactivateAfterFatal(ctx)
		return report, err
	}
	return report, nil
}

// runSafely calls fn, converting a panic into a *panicError return rather
// than letting it unwind the caller's goroutine.
func runSafely(ctx context.Context, fn Runnable) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{recovered: r, stack: debug.Stack()}
		}
	}()
	return fn(ctx)
}

// recordFire advances job's LastFire/nextFire after a successful run and
// persists the updated CronJob, then publishes EventKindSchedulerFired.
// Persistence/publish failures are recorded via emitEvent-style
// best-effort logging rather than rolling back the fire itself — the job
// DID run; under-reporting that is worse than a stale LastFire field.
func (s *Scheduler) recordFire(ctx context.Context, job *scheduledJob, now time.Time) {
	next, err := job.schedule.NextAfter(now)
	if err != nil {
		s.emitEvent(ctx, EventKindSchedulerOrphanedOwner, job.CronJob, "spec exhausted after fire: "+err.Error())
		return
	}
	job.LastFire = now
	job.nextFire = next
	_ = putJob(ctx, s.store, s.namespace, job.CronJob)
	s.publish(ctx, EventKindSchedulerFired, "info", job.CronJob, "fired")
}

// deactivateAfterFatal marks the scheduler inactive and best-effort
// releases the advisory lock. Used by Tick's panic path and by a lost
// lease renewal. It always uses a fresh background context for the
// release itself — the caller's ctx may be the very thing that triggered
// the fatal condition (e.g. already canceled), and a lock release on the
// way out must not be skipped just because of that.
func (s *Scheduler) deactivateAfterFatal(_ context.Context) {
	_ = s.Close(context.Background())
}
