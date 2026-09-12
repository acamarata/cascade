package jobs

// Purpose: Scheduler.Advance's dispatch (the CancelRequested,
//
//	TerminationConfirmed and LeaseExpired branches handled inline; the
//	LeaseAcquired/JobTransitioned/LeaseReclaimed branches delegate to
//	scheduler_admit.go's admitNodes) plus the Reader seam Resume replays
//	through.
//
// Inputs: an ExecutionDag (dag.go), an Event (scheduler_types.go), the
//
//	caller's current view of every relevant job's state, the active
//	leases the caller holds, and a GovernorFn.
//
// Outputs: a ScheduleDelta -- never a panic, never a bare error return
//
//	(Advance's own signature carries no error; failures land in
//	ScheduleDelta.Errors so a partial delta is always usable, per HOW 2d
//	"fail-closed but non-panicking").
//
// CONTRACT NOTE (files_scope, quoted in the journal): the full_desc names
// Advance's signature as (dag, event, activeLeases, governorFn) with no
// context.Context and no job-state input. Both are load-bearing gaps
// this file closes rather than papering over: (1) GovernorFn's own type
// is func(context.Context, ...) -- Advance cannot call it without a ctx,
// so ctx is Advance's first parameter; (2) HOW 2c's admissibility rule
// ("all deps[] are ACCEPTED") requires each dependency's current
// JobState, and neither ExecutionDag (dag.go's DagNode carries no state
// field -- ExecutionDag is the PLANNER's static output) nor a single
// incoming Event supplies that for every node in the DAG, so Advance
// takes an explicit jobStates map. Both additions are the minimum needed
// for the acceptance criteria to be checkable at all.
//
// SPORT: jobs/scheduler/ADD (P1-E29-W6-S59-T5).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// JournalEntry is one replayed M/S-27.T1 journal record, in the shape
// Resume needs: enough to detect a stale `running` job without this
// package importing internal/fleet/journal directly (boundary preserved
// per HOW 3's "no direct import" requirement).
type JournalEntry struct {
	EntityID string
	Cursor   int64
	Kind     string
	Payload  []byte
}

// Reader is the Journal seam Resume replays through. The caller wires
// this to the real M/S-27.T1 journal.Store.Replay; the HOW 3 "Reader
// interface {Replay(ctx, entityID, cursor) iterator}" prose names an
// iterator return shape, but every existing Replay-like reader in this
// tree (internal/fleet/journal/replay.go's own Replay) returns a
// materialized slice, not a Go iterator type -- this seam follows that
// precedent rather than inventing a novel iterator contract the real
// implementation would then have to adapt to.
type Reader interface {
	Replay(ctx context.Context, entityID string, cursor int64) ([]JournalEntry, error)
}

// Advance is the AC/S-59.T5 scheduling primitive: given the current dag,
// one Event, every relevant job's current JobState, the leases active
// right now, and the admission seam, it returns the schedule delta the
// coordinator should apply. Pure: no side effects, no DB calls, no lease
// acquisition, no direct import of the concrete governor.
// AdmissionController.
func (s *Scheduler) Advance(ctx context.Context, dag ExecutionDag, event Event, jobStates map[string]JobState, activeLeases []ResourceLease, governorFn GovernorFn) ScheduleDelta {
	if err := requireControllerCtx(ctx, "job.advance"); err != nil {
		return ScheduleDelta{Errors: []error{err}}
	}
	switch e := event.(type) {
	case CancelRequested:
		return advanceCancel(e, jobStates)
	case TerminationConfirmed:
		return advanceTermination(e, jobStates)
	case LeaseExpired:
		return advanceLeaseExpired(e)
	case LeaseAcquired, JobTransitioned, LeaseReclaimed:
		return s.admitNodes(ctx, dag, jobStates, activeLeases, governorFn)
	default:
		return ScheduleDelta{Errors: []error{
			cascade.Newf(cascade.KindInvalidInput, "jobs: scheduler: unknown event type %T", event),
		}}
	}
}

// advanceCancel implements HOW 2a's idempotent-cancel rule.
func advanceCancel(e CancelRequested, jobStates map[string]JobState) ScheduleDelta {
	state, ok := jobStates[e.JobID]
	if !ok {
		return ScheduleDelta{Errors: []error{
			cascade.Newf(cascade.KindInvalidInput, "jobs: scheduler: cancel requested for unknown job %q", e.JobID),
		}}
	}
	if state.Terminal() || state == JobStateCancelling {
		return ScheduleDelta{} // idempotent: already terminal or termination in flight
	}
	return ScheduleDelta{
		JobsToAdvance: []StateTransition{{JobID: e.JobID, From: state, To: JobStateCancelling}},
		OutboxIntents: []OutboxIntent{{JobID: e.JobID, Site: OutboxSiteIntegration, AttemptGeneration: 0, PayloadHash: cancelPayloadHash(e.JobID)}},
	}
}

// advanceTermination implements HOW 2a's ONLY path to the terminal
// `cancelled` state: a confirmed process-group exit (or an executor
// reconciliation reporting no live writer), never a bare cancel request.
func advanceTermination(e TerminationConfirmed, jobStates map[string]JobState) ScheduleDelta {
	state, ok := jobStates[e.JobID]
	if !ok || state != JobStateCancelling {
		// A confirmation for a job not currently cancelling is not this
		// scheduler's concern to mutate (idempotent no-op) -- fail-closed
		// by doing nothing rather than guessing at a transition.
		return ScheduleDelta{}
	}
	return ScheduleDelta{
		JobsToAdvance: []StateTransition{{JobID: e.JobID, From: JobStateCancelling, To: JobStateCancelled}},
	}
}

// advanceLeaseExpired implements HOW 2b: never advance state, always
// surface an attention item so the coordinator (or a human) sees it.
func advanceLeaseExpired(e LeaseExpired) ScheduleDelta {
	return ScheduleDelta{
		EventsToEmit: []Event{AttentionRaised{JobID: e.JobID, Reason: "lease expired: " + e.RepoID + "/" + e.ScopeGlob}},
	}
}
