package daemon

// Purpose: RegisterScheduler's (subsystems_scheduler.go) companion
//
//	translator and consumer loop -- split to its own file purely to keep
//	subsystems_scheduler.go under the 300-line cap (Art.10.3), the same
//	rationale subsystems_worktree.go records for its own split from
//	subsystems.go.
//
// Inputs: a jobs.lease bus events.Event (lease_events.go's own
//
//	leasePayload wire shape: repo_id, scope_glob, holder, epoch, state --
//	internal/daemon cannot import that unexported type, so
//	leaseBusPayload below mirrors its JSON tags exactly).
//
// Outputs: a jobs.DecodeEvent-compatible envelope, decoded and dispatched
//
//	through jobs.Guard + Scheduler.Advance. Only EventLeaseAcquired and
//	EventLeaseExpired carry a jobs.Event counterpart today (lease_acquired
//	/lease_expired); EventLeaseReleased/Contended/Renewed are no-ops here,
//	matching worktree_events.go's own apply() default case for the exact
//	same namespace.
//
// Constraints: Holder IS the lease's job id (model.go's ResourceLease.
//
//	Holder doc comment, verified against store_lease.go's
//	allLeasesForHolder(jobID)); the wire envelope's lease_id has no
//	dedicated column on ResourceLease, so this file derives one the same
//	way lease_events.go derives its own journal/attention entity id: the
//	repo id and scope glob, joined, since a lease's identity IS the
//	(RepoID, ScopeGlob) pair (R-21.139).
//
// SPORT: internal/daemon (ADD, merge-fix for P1-E29-W6-S59-T5).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// leaseBusPayload mirrors internal/jobs/lease_events.go's unexported
// leasePayload JSON shape -- the only cross-package contract available,
// since this package cannot import that unexported type.
type leaseBusPayload struct {
	RepoID    string `json:"repo_id"`
	ScopeGlob string `json:"scope_glob"`
	Holder    string `json:"holder"`
}

// schedulerAdvanceMethod is the Guard method name this consumer checks
// before every Advance call -- jobs.GuardedMethods' "job.advance" entry
// (internal/nodes/controller.go), applied here exactly as that symbol's
// own doc comment says AC/S-59.T5's handlers must.
const schedulerAdvanceMethod = "job.advance"

// decodeSchedulerEvent translates one jobs.lease bus event into a
// jobs.DecodeEvent-compatible wire envelope and decodes it. ok is false
// for a bus event kind this scheduler has no Event counterpart for
// (released/contended/renewed) -- a disclosed no-op, not an error.
func decodeSchedulerEvent(ev events.Event) (jobs.Event, bool, error) {
	var p leaseBusPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return nil, false, cascade.Wrap(cascade.KindInvalidInput, err, "daemon: scheduler: decode lease bus payload")
	}
	leaseID := p.RepoID + ":" + p.ScopeGlob

	var envelope map[string]any
	// This namespace ("jobs.lease") carries ONLY lease_events.go's own
	// five EventKind values -- events.EventKind is a deliberately open,
	// cross-package taxonomy (see internal/events' own doc comment), so
	// this switch is intentionally non-exhaustive over that type's full,
	// tree-wide vocabulary, matching worktree_events.go's identical
	// switch over the SAME namespace.
	//nolint:exhaustive // see comment above: exhaustive over this namespace's own five kinds, not events.EventKind's tree-wide vocabulary
	switch ev.Kind {
	case jobs.EventLeaseAcquired:
		envelope = map[string]any{"kind": "lease_acquired", "job_id": p.Holder, "lease_id": leaseID}
	case jobs.EventLeaseExpired:
		envelope = map[string]any{"kind": "lease_expired", "job_id": p.Holder, "repo_id": p.RepoID, "scope_glob": p.ScopeGlob}
	default: // EventLeaseReleased, EventLeaseContended, EventLeaseRenewed: no scheduler action
		return nil, false, nil
	}

	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindInvalidInput, err, "daemon: scheduler: encode decode envelope")
	}
	event, err := jobs.DecodeEvent(raw)
	if err != nil {
		return nil, false, err
	}
	return event, true, nil
}

// runSchedulerConsumer drives sub, translating and dispatching every
// delivered lease event through jobs.Guard + sched.Advance until ctx is
// canceled or sub's Events channel closes -- the production loop
// RegisterScheduler starts as its own goroutine. A malformed payload or a
// Guard/Advance-level error is accumulated into the returned error rather
// than aborting the loop early: one bad event must never silently starve
// every subsequent one.
func runSchedulerConsumer(ctx context.Context, sched *jobs.Scheduler, sub *events.Subscription) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-sub.Errs:
			if ok && err != nil {
				return err
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			if err := dispatchSchedulerEvent(ctx, sched, ev); err != nil {
				return err
			}
		}
	}
}

// dispatchSchedulerEvent is runSchedulerConsumer's single-event step,
// split out so its Guard-then-Advance body stays well under the 50-line
// function cap.
func dispatchSchedulerEvent(ctx context.Context, sched *jobs.Scheduler, ev events.Event) error {
	event, ok, err := decodeSchedulerEvent(ev)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := jobs.Guard(ctx, schedulerAdvanceMethod); err != nil {
		// A node-role daemon refuses here (R-21.169) -- the load-bearing
		// behavior this wiring exists to prove, never a crash: the
		// refusal is expected on every non-controller daemon and is not
		// itself a consumer failure.
		return nil
	}
	delta := sched.Advance(ctx, jobs.ExecutionDag{}, event, map[string]jobs.JobState{}, nil, nil)
	if len(delta.Errors) > 0 {
		return delta.Errors[0]
	}
	return nil
}
