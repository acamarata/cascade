// Package scheduler implements the daemon's persisted cron scheduler
// (02-TARGET-STRUCTURE.md: "events/  # bus, hooks, scheduler"): CronJob
// definitions survive daemon restart via provider.Store, exactly one
// daemon process may run the scheduler against a given Store namespace at
// a time (the advisory lock, lock.go), and every successful job fire is
// published to the internal/events Bus (C/S-04.T3) — never to a journal,
// which M/S-27.T1 wires later.
//
// # Persistence and domain placement
//
// R-14.5 closes the cascade.db domain set at ten members; scheduler has
// no domain of its own, exactly like the event bus (bus.go's package
// doc). Scheduler.New's namespace argument is the SAME Store namespace
// the caller wires the bus to — per R-14.148's precedent, the "queue"
// domain (internal/storage/queue's DomainQueue) — and this package's own
// "sched:job:"/"sched:lock" key prefixes (job.go, lock.go) keep its
// records apart from the bus's "event:"/cursor records sharing that same
// namespace.
//
// # Skip-missed scheduling
//
// Every occurrence this package computes — at Activate and at every
// subsequent Tick — is "the next occurrence strictly after clock.Now()",
// never "every occurrence since LastFire". A job whose daemon was down
// across N scheduled windows fires exactly once, for the next valid
// window, not N times in a burst: Schedule.NextAfter (cron.go) always
// takes the CURRENT instant as its `from` argument, never LastFire or an
// accumulated backlog. This is what makes a long outage safe by
// construction rather than by a special case.
//
// # No internal ticking goroutine
//
// Scheduler does not run its own wall-clock timer. Tick must be called by
// the caller (a real time.Ticker in production composition-root wiring,
// or directly by a test after advancing a frozen clock) — this is what
// lets every test in this package be fully deterministic with zero
// sleeps (Art.7.3, R-14.136): a test controls exactly when "time passes"
// by advancing the injected Clock and then calling Tick itself. Activate
// does start exactly one small background goroutine, watchCancellation,
// whose only job is to release the advisory lock when the context passed
// to Activate is canceled (see Close's doc comment for the three ways a
// held lease actually goes away).
//
// # Overrun policy: SKIP
//
// Tick is safe to call concurrently with itself (e.g. a slow-running job
// still executing when the next ticker interval elapses in the caller):
// a Tick call that arrives while a previous Tick call on the same
// Scheduler is still in flight returns immediately (TickReport.Skipped)
// without firing anything. This never causes two concurrent runs of the
// same job, and it never queues up extra firings — the next Tick call
// after the in-flight one completes just evaluates jobs against
// clock.Now() at that later instant, exactly as skip-missed scheduling
// already guarantees for a caller that skips calling Tick for a long
// stretch.
//
// SPORT: internal.events.scheduler.Scheduler/ADDED (P1-E03-W1-S04-T4).
package scheduler

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrNotActivated is returned by Tick when called before a successful
// Activate (or after the scheduler has since deactivated).
var ErrNotActivated = cascade.New(cascade.KindInvalidInput, "scheduler: Tick called before a successful Activate")

// Runnable is the function a registered job Owner executes when its
// CronJob fires. A non-nil error is recorded and published as an
// EventKindSchedulerJobFailed event but is NOT fatal to the scheduler — a
// panic inside Runnable is what Scheduler treats as fatal (see Tick).
type Runnable func(ctx context.Context) error

// scheduledJob is one Activate-computed, in-memory view of a persisted
// CronJob: its parsed Schedule and the next instant it is due to fire.
type scheduledJob struct {
	CronJob
	schedule Schedule
	nextFire time.Time
}

// ActivateReport summarizes one Activate call.
type ActivateReport struct {
	// Scheduled lists every persisted job Activate successfully loaded
	// and scheduled (its Owner has a registered Runnable).
	Scheduled []CronJob
	// Orphaned lists every persisted job whose Owner has no registered
	// Runnable — never silently dropped; see OrphanedJobs.
	Orphaned []CronJob
}

// TickReport summarizes one Tick call.
type TickReport struct {
	// Skipped is true when this Tick call returned immediately because a
	// previous Tick call on the same Scheduler was still in flight
	// (Overrun policy: SKIP). No other field is meaningful when Skipped
	// is true.
	Skipped bool
	// Fired lists the IDs of every job this Tick call attempted to run
	// (successfully or not).
	Fired []string
	// Errors lists every non-nil error a fired job produced, in firing
	// order. A panic-derived error is always the LAST entry (Tick stops
	// firing further jobs once one panics).
	Errors []error
}

// Scheduler is the daemon's persisted cron scheduler. The zero value is
// not usable; construct with New.
type Scheduler struct {
	store     provider.Store
	namespace string
	clock     runtime.Clock
	bus       *events.Bus
	ownerID   string
	leaseTTL  time.Duration
	lock      *advisoryLock

	mu         sync.Mutex
	active     bool
	ticking    bool
	runnables  map[string]Runnable
	jobs       map[string]*scheduledJob
	orphaned   []CronJob
	done       chan struct{}
	doneOnce   func()
	cancelWait context.CancelFunc
}

// New returns a ready-to-use Scheduler. namespace is the Store namespace
// this instance persists jobs and its advisory lock through (see the
// package doc's "Persistence and domain placement"). ownerID identifies
// THIS daemon process instance for advisory-lock ownership — it must be
// distinct across concurrently-running daemon processes (e.g. a UUID
// generated once at process start) for the lock to mean anything.
// leaseTTL is the advisory lock's lease duration; 08-INIT-CONFIG-SPEC.md
// §3 declares no scheduler config section, so per R-14.107's "invent no
// defaults" precedent this package does not silently pick one — the
// caller must supply an explicit, positive leaseTTL (validated at
// Activate, not here, matching this codebase's existing convention of
// validating at the operation that needs the value rather than at
// construction — see e.g. RetentionConfig.Clock).
func New(store provider.Store, namespace string, clock runtime.Clock, bus *events.Bus, ownerID string, leaseTTL time.Duration) *Scheduler {
	return &Scheduler{
		store:     store,
		namespace: namespace,
		clock:     clock,
		bus:       bus,
		ownerID:   ownerID,
		leaseTTL:  leaseTTL,
		runnables: make(map[string]Runnable),
	}
}

// RegisterRunnable associates owner with fn. It must be called before
// Activate for any persisted job naming that owner to be scheduled rather
// than reported as orphaned. Registering the same owner twice replaces
// the previous Runnable.
func (s *Scheduler) RegisterRunnable(owner string, fn Runnable) error {
	if owner == "" {
		return cascade.New(cascade.KindInvalidInput, "scheduler: RegisterRunnable requires a non-empty owner")
	}
	if fn == nil {
		return cascade.New(cascade.KindInvalidInput, "scheduler: RegisterRunnable requires a non-nil Runnable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runnables[owner] = fn
	return nil
}

// ScheduleJob upserts a persisted CronJob definition. spec is validated
// via ParseSpec before anything is written — a malformed spec is rejected
// here, at declaration time, rather than surfacing later as a corrupt
// record. Re-scheduling an existing id preserves its LastFire (skip-missed
// scheduling already makes LastFire purely observational, but there is no
// reason to discard a real audit value on a routine re-declaration, e.g.
// the same daemon re-registering its built-in jobs on every startup).
func (s *Scheduler) ScheduleJob(ctx context.Context, id, spec, owner string) error {
	if id == "" || owner == "" {
		return cascade.New(cascade.KindInvalidInput, "scheduler: ScheduleJob requires a non-empty id and owner")
	}
	if _, err := ParseSpec(spec); err != nil {
		return err
	}
	existing, err := getJob(ctx, s.store, s.namespace, id)
	switch {
	case err == nil:
		existing.Spec, existing.Owner = spec, owner
	case cascade.HasKind(err, cascade.KindNotFound):
		existing = CronJob{ID: id, Spec: spec, Owner: owner}
	default:
		return err
	}
	return putJob(ctx, s.store, s.namespace, existing)
}

// Active reports whether the scheduler currently holds its advisory lock
// and is ready to Tick.
func (s *Scheduler) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Done returns a channel that closes exactly once, when the scheduler
// transitions from active to inactive — whether via an explicit Close, a
// canceled Activate context, or a fatal in-Tick error. Callers (and
// tests) that need to wait deterministically for lock release use
// <-sched.Done() rather than a sleep or a poll (Art.7.3, R-14.136). Done
// returns nil before the first successful Activate.
func (s *Scheduler) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// OrphanedJobs returns the jobs the most recent Activate found whose
// Owner has no registered Runnable — the surface a future doctor
// scheduler check (and the R/S-39 attention queue) reads, per this
// ticket's Art.1 "never silently skipped" requirement.
func (s *Scheduler) OrphanedJobs() []CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CronJob, len(s.orphaned))
	copy(out, s.orphaned)
	return out
}

// ListScheduledJobs returns every job currently persisted in the
// scheduler's Store namespace, sorted by ID — the ground truth regardless
// of Activate state, which is what
// TestRegisterRetentionJobs_WeeklyInterval (retention_register_test.go)
// asserts against directly.
func (s *Scheduler) ListScheduledJobs() ([]CronJob, error) {
	return listJobs(context.Background(), s.store, s.namespace)
}

// validate reports a cascade.KindInvalidInput error for any required
// field New did not receive, checked once at Activate (see New's doc
// comment for why validation is deferred to here).
func (s *Scheduler) validate() error {
	switch {
	case s.store == nil:
		return cascade.New(cascade.KindInvalidInput, "scheduler: New requires a non-nil Store")
	case s.namespace == "":
		return cascade.New(cascade.KindInvalidInput, "scheduler: New requires a non-empty namespace")
	case s.clock == nil:
		return cascade.New(cascade.KindInvalidInput, "scheduler: New requires a non-nil Clock")
	case s.bus == nil:
		return cascade.New(cascade.KindInvalidInput, "scheduler: New requires a non-nil Bus")
	case s.ownerID == "":
		return cascade.New(cascade.KindInvalidInput, "scheduler: New requires a non-empty ownerID")
	case s.leaseTTL <= 0:
		return cascade.New(cascade.KindInvalidInput, "scheduler: New requires a positive leaseTTL")
	default:
		return nil
	}
}

// dueJobsLocked returns every in-memory scheduled job whose nextFire is
// not after now, sorted by ID for deterministic firing order. Caller MUST
// hold s.mu.
func dueJobsLocked(jobs map[string]*scheduledJob, now time.Time) []*scheduledJob {
	var due []*scheduledJob
	for _, j := range jobs {
		if !j.nextFire.After(now) {
			due = append(due, j)
		}
	}
	sort.Slice(due, func(i, k int) bool { return due[i].ID < due[k].ID })
	return due
}
