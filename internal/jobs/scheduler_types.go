package jobs

// Purpose: the AC/S-59.T5 DAG scheduler's typed vocabulary -- the Event
//
//	union Advance consumes, ScheduleDelta (Advance's pure output),
//	LeaseIntent/StateTransition (ScheduleDelta's element types),
//	GovernorFn (the sole admission injection seam, R-16.64) and the
//	stateless Scheduler struct itself.
//
// Inputs: DecodeEvent's raw JSON payload (FuzzScheduleEvent's target);
//
//	every other type here is constructed directly by callers/tests.
//
// Outputs: a decoded Event, or a typed KindInvalidInput error for an
//
//	unparseable payload or an unknown "kind" discriminator -- never a
//	permissive zero value (Art.1/06 rule 7).
//
// Constraints: this file imports internal/fleet/governor ONLY for the
//
//	AdmissionRequest/Permit types GovernorFn's signature names, never the
//	concrete AdmissionController -- Advance stays pure and DB-free with
//	governorFn as its one admission seam (R-16.64). Scheduler holds no
//	mutable state; every call carries its own dag/event/state arguments.
//
// CONTRACT NOTE (files_scope, quoted in the journal): this ticket's task
// list names four Event variants (LeaseAcquired, JobTransitioned,
// CancelRequested, LeaseExpired), but its own HOW section 2(a) requires
// acting on a "TerminationConfirmed event" (the only trigger for a
// terminal `cancelled` transition) and its HOW 2(b)/(c) requires a
// "LeaseReclaimed event" (the only trigger that frees an
// expired_unconfirmed scope). Both are real, load-bearing inputs the
// acceptance criteria depend on, so the union here has SIX variants, not
// four; the two additions are documented, not silent. A seventh,
// AttentionRaised, is not an Advance INPUT -- it is the "attention-item
// event" HOW 2(b) requires Advance to append to EventsToEmit on
// LeaseExpired, reusing the same Event union for scheduler OUTPUT so
// ScheduleDelta.EventsToEmit stays one typed slice rather than two.
//
// SPORT: jobs/scheduler/ADD (P1-E29-W6-S59-T5).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Event is the closed union Advance dispatches on. The unexported method
// seals the interface to this file's variants -- no permissive zero
// value, no external implementer, matching state.go's JobState/
// glob.go's Scope sealing convention in this same package.
type Event interface {
	eventKind() string
}

// LeaseAcquired reports that leaseID was granted for jobID -- one of the
// two "re-evaluate admissibility" triggers (the other is
// JobTransitioned).
type LeaseAcquired struct {
	JobID   string
	LeaseID string
}

func (LeaseAcquired) eventKind() string { return "lease_acquired" }

// JobTransitioned reports jobID moved from From to To -- typically a dep
// reaching JobStateAccepted, which may newly admit downstream nodes.
type JobTransitioned struct {
	JobID string
	From  JobState
	To    JobState
}

func (JobTransitioned) eventKind() string { return "job_transitioned" }

// CancelRequested asks the scheduler to begin cancelling jobID (HOW 2a,
// R-21.140/R-21.174).
type CancelRequested struct {
	JobID string
}

func (CancelRequested) eventKind() string { return "cancel_requested" }

// LeaseExpired reports that the lease at (RepoID, ScopeGlob) held by
// JobID passed its TTL without renewal (HOW 2b, R-21.139/R-21.177).
type LeaseExpired struct {
	JobID     string
	RepoID    string
	ScopeGlob string
}

func (LeaseExpired) eventKind() string { return "lease_expired" }

// TerminationConfirmed reports the AD/S-61.T1 driver confirmed jobID's
// process-group exit (or an executor reconciliation found no live
// writer) -- the ONLY trigger for the terminal `cancelled` transition
// (HOW 2a, R-21.174).
type TerminationConfirmed struct {
	JobID string
}

func (TerminationConfirmed) eventKind() string { return "termination_confirmed" }

// LeaseReclaimed reports the S-59.T2 reclaim path confirmed the prior
// holder's death and advanced the epoch at (RepoID, ScopeGlob) to
// NewEpoch -- the ONLY event that frees an expired_unconfirmed scope for
// new admission (HOW 2b, R-21.139/R-21.177).
type LeaseReclaimed struct {
	RepoID    string
	ScopeGlob string
	NewEpoch  int64
}

func (LeaseReclaimed) eventKind() string { return "lease_reclaimed" }

// AttentionRaised is an OUTPUT-only Event: Advance appends one to
// ScheduleDelta.EventsToEmit on LeaseExpired (HOW 2b: "never silently
// drop or steal") so the coordinator can route it to the real
// supervision attention queue (lease_events.go's leaseEventSink already
// owns the actual Push -- this is the scheduler's own pass-through
// signal, not a second write of the same item).
type AttentionRaised struct {
	JobID  string
	Reason string
}

func (AttentionRaised) eventKind() string { return "attention_raised" }

// LeaseIntent is one admitted node's lease-acquisition instruction: the
// (repo id, normalized prefix scope) and the lease epoch Advance
// observed, so the coordinator's acquire is FENCED (S-59.T2 step 10).
// Permit is the governor grant for this admission; the coordinator calls
// Permit.Release() when JobID reaches a terminal state (HOW 2c) --
// ownership transfers to the caller because Advance itself is stateless
// and cannot hold it across calls.
type LeaseIntent struct {
	JobID     string
	RepoID    string
	ScopeGlob string
	Epoch     int64
	Permit    governor.Permit
}

// StateTransition is one JobsToAdvance entry: a caller-visible job state
// change Advance decided but did not itself persist (Advance makes no DB
// calls; the coordinator's store write is what actually lands it,
// consistent with store_job.go's own TransitionAllowed-only mutator
// path).
type StateTransition struct {
	JobID string
	From  JobState
	To    JobState
}

// OutboxIntent is the R-21.148 outbox row Advance decided must be
// recorded in the SAME store transaction as a StateTransition -- e.g.
// the CancelRequested path's "outbox intent for the driver cancel
// effect" (HOW 2a). Advance cannot write this itself (no DB calls); the
// coordinator persists it via outbox.go's RecordIntent inside the same
// transaction as the paired StateTransition.
type OutboxIntent struct {
	JobID             string
	Site              OutboxSite
	AttemptGeneration int64
	PayloadHash       string
}

// ScheduleDelta is Advance's entire output: the pure function's
// decisions, never yet applied to any store.
type ScheduleDelta struct {
	LeasesToAcquire []LeaseIntent
	JobsToAdvance   []StateTransition
	OutboxIntents   []OutboxIntent
	EventsToEmit    []Event
	Errors          []error
}

// GovernorFn is the ONE admission API this package calls (R-16.64): the
// same signature K/S-23.T2 injects into Conductor's fan-out. Advance
// imports only AdmissionRequest/Permit from internal/fleet/governor,
// never the concrete AdmissionController, keeping this package's own
// admission seam func-typed.
type GovernorFn func(context.Context, governor.AdmissionRequest) (governor.Permit, error)

// Scheduler is the stateless AC/S-59.T5 scheduling authority. clock is
// its only field -- an immutable constructor-injected dependency, not
// mutable state (Resume's staleness math needs "now"; Advance never
// reads it). Construct with NewScheduler; the zero value's nil clock is
// not usable.
type Scheduler struct {
	clock runtime.Clock
}

// NewScheduler constructs a Scheduler. clk must not be nil in
// production (runtime.NewSystemClock()); tests inject a fixed clock
// (Art.7.3 -- no bare time.Now).
func NewScheduler(clk runtime.Clock) *Scheduler {
	return &Scheduler{clock: clk}
}

// eventEnvelope is DecodeEvent's wire shape: a "kind" discriminator plus
// every variant's fields as optional members. FuzzScheduleEvent decodes
// arbitrary bytes through this shape and must never panic (06 §5 rule
// 7).
type eventEnvelope struct {
	Kind      string `json:"kind"`
	JobID     string `json:"job_id,omitempty"`
	LeaseID   string `json:"lease_id,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	RepoID    string `json:"repo_id,omitempty"`
	ScopeGlob string `json:"scope_glob,omitempty"`
	NewEpoch  int64  `json:"new_epoch,omitempty"`
}

// ErrUnknownEvent is the sentinel DecodeEvent wraps for an unrecognized
// "kind" discriminator -- fail-closed, no permissive zero value in the
// Event union (Art.1).
var ErrUnknownEvent = cascade.New(cascade.KindInvalidInput, "jobs: scheduler: unknown event kind")

// DecodeEvent parses raw (JSON) bytes into one of the six INPUT Event
// variants. Malformed JSON and an unrecognized "kind" both fail closed
// with a typed KindInvalidInput error -- never a panic, never a
// permissive default (Art.1, 06 §5 rule 7). AttentionRaised is
// OUTPUT-only and never decodes from the wire.
func DecodeEvent(data []byte) (Event, error) {
	var env eventEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "jobs: scheduler: malformed event payload")
	}
	switch env.Kind {
	case "lease_acquired":
		return LeaseAcquired{JobID: env.JobID, LeaseID: env.LeaseID}, nil
	case "job_transitioned":
		return JobTransitioned{JobID: env.JobID, From: JobState(env.From), To: JobState(env.To)}, nil
	case "cancel_requested":
		return CancelRequested{JobID: env.JobID}, nil
	case "lease_expired":
		return LeaseExpired{JobID: env.JobID, RepoID: env.RepoID, ScopeGlob: env.ScopeGlob}, nil
	case "termination_confirmed":
		return TerminationConfirmed{JobID: env.JobID}, nil
	case "lease_reclaimed":
		return LeaseReclaimed{RepoID: env.RepoID, ScopeGlob: env.ScopeGlob, NewEpoch: env.NewEpoch}, nil
	default:
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownEvent, "jobs: scheduler: unknown event kind %q", env.Kind)
	}
}
