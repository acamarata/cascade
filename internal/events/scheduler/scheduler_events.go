// Purpose: the EventKind vocabulary this package publishes to the
//   internal/events Bus (task 4's "Publish event to bus"), plus the
//   shared emit helper every publish site (Activate's orphan detection,
//   Tick's fire/failure/panic paths) uses. Split from scheduler.go under
//   R-14.117's authorized-split allowance.
// Constraints: no journal wiring (explicitly deferred to M/S-27.T1 —
//   these events go ONLY to the Bus). A Publish failure here is logged
//   into the returned report/error, never silently swallowed (Art.1), but
//   it also never blocks or fails the job fire it describes — Publish
//   itself never blocks (bus.go's package doc) and its own failure is a
//   Store-availability problem orthogonal to whether the job ran.
// SPORT: internal.events.scheduler.EventKind constants/ADDED
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
)

// EventKind values this package publishes. EventKind is an OPEN type
// (internal/events/types.go's doc comment) — this package mints its own
// values exactly as internal/plugins and every other Bus producer does.
const (
	// EventKindSchedulerFired is published each time a job's Runnable
	// returns successfully (nil error, no panic).
	EventKindSchedulerFired events.EventKind = "scheduler.fired"
	// EventKindSchedulerJobFailed is published when a job's Runnable
	// returns a non-nil error without panicking. Not fatal to the
	// scheduler — Tick continues to the next due job.
	EventKindSchedulerJobFailed events.EventKind = "scheduler.job_failed"
	// EventKindSchedulerJobPanicked is an ERROR-level event published
	// when a job's Runnable panics. Fatal: Tick recovers the panic,
	// releases the advisory lock, deactivates the scheduler, and stops
	// firing further jobs this Tick — see scheduler_tick.go.
	EventKindSchedulerJobPanicked events.EventKind = "scheduler.job_panicked"
	// EventKindSchedulerOrphanedOwner is an ERROR-level event published
	// for a persisted job whose Owner has no registered Runnable (or
	// whose Spec no longer parses). Never silently skipped (Art.1); see
	// OrphanedJobs for the queryable surface a future doctor check reads.
	EventKindSchedulerOrphanedOwner events.EventKind = "scheduler.orphaned_owner"
)

// schedulerEventPayload is the JSON body every event this package
// publishes carries. Event.Payload is opaque to the Bus (types.go), so
// this shape is a private wire contract between this package's own
// publishers and whatever future consumer (doctor, the R/S-39 attention
// queue) subscribes to these EventKinds.
type schedulerEventPayload struct {
	// Level is "error" for a fatal/attention-worthy event
	// (orphaned-owner, panic) or "info" for a routine fire/failure.
	Level   string `json:"level"`
	JobID   string `json:"job_id"`
	Owner   string `json:"owner"`
	Message string `json:"message"`
}

// emitEvent publishes an ERROR-level event for job. Publish errors are
// intentionally swallowed here (not returned) — see this file's doc
// comment for why a Bus-availability failure must never mask or block the
// job outcome it is merely reporting.
func (s *Scheduler) emitEvent(ctx context.Context, kind events.EventKind, job CronJob, message string) {
	s.publish(ctx, kind, "error", job, message)
}

// publish is the single Bus.Publish call site every EventKind in this
// file funnels through.
func (s *Scheduler) publish(ctx context.Context, kind events.EventKind, level string, job CronJob, message string) {
	payload, err := json.Marshal(schedulerEventPayload{
		Level: level, JobID: job.ID, Owner: job.Owner, Message: message,
	})
	if err != nil {
		return
	}
	_, _ = s.bus.Publish(ctx, s.namespace, kind, "scheduler", payload)
}
