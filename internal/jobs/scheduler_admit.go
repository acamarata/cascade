package jobs

// Purpose: HOW 2c's admissibility rule, deterministic ordering, and the
//
//	no-per-node-permit admission rule (P1-TOP-04).
//
// Inputs: an ExecutionDag, the caller's jobStates map, the active leases
//
//	in scope right now. governorFn is accepted and never called.
//
// Outputs: a ScheduleDelta whose LeasesToAcquire holds one entry per
//
//	admitted node, in priority-desc/id-asc order; Errors accumulates a
//	malformed-scope failure per node without aborting the batch.
//
// Constraints: a node whose deps are not all ACCEPTED, or whose
//
//	mutable_scope intersects any Contending() active lease (including
//	expired_unconfirmed, R-21.139), is never admitted. No governor
//	Admit call is made per node; capacity admission is the reservation
//	ledger's job (Readmit, scheduler_readmit.go).
//
// SPORT: jobs/scheduler/ADD (P1-E29-W6-S59-T5).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// admitNodes implements HOW 2c: collect admissible nodes and order them
// deterministically. It takes no governor permit per node (P1-TOP-04):
// admission control lives on the reservation path (ReserveFn/AdoptFn),
// so no permit is ever held while a reservation is parked. The
// GovernorFn parameter is kept only so Advance's signature is unchanged;
// it is never called here.
func (s *Scheduler) admitNodes(_ context.Context, dag ExecutionDag, jobStates map[string]JobState, activeLeases []ResourceLease, _ GovernorFn) ScheduleDelta {
	var delta ScheduleDelta
	admissible, scopes := collectAdmissible(dag, jobStates, activeLeases, &delta)

	sort.SliceStable(admissible, func(i, j int) bool {
		if admissible[i].Priority != admissible[j].Priority {
			return admissible[i].Priority > admissible[j].Priority
		}
		return admissible[i].ID < admissible[j].ID
	})

	for _, n := range admissible {
		scope := scopes[n.ID]
		repoID, epoch := observedFence(scope, activeLeases)
		if jobStates[n.ID] == JobStateLeased {
			// R-16.68c resume re-entry: this job already holds its lease
			// (Resume re-entered it at `leased`); admission here moves it
			// straight to `running`. The coordinator re-admits its
			// reservation through Readmit before applying the transition.
			delta.JobsToAdvance = append(delta.JobsToAdvance, StateTransition{JobID: n.ID, From: JobStateLeased, To: JobStateRunning})
		}
		delta.LeasesToAcquire = append(delta.LeasesToAcquire, LeaseIntent{
			JobID: n.ID, RepoID: repoID, ScopeGlob: scope.String(), Epoch: epoch,
		})
	}
	return delta
}

// collectAdmissible implements HOW 2c's admissibility rule (deps
// ACCEPTED, scope free of another holder's contending lease) -- the
// admitNodes funlen-cap split.
func collectAdmissible(dag ExecutionDag, jobStates map[string]JobState, activeLeases []ResourceLease, delta *ScheduleDelta) ([]DagNode, map[string]Scope) {
	admissible := make([]DagNode, 0, len(dag.Nodes))
	scopes := make(map[string]Scope, len(dag.Nodes))
	for _, n := range dag.Nodes {
		// A node whose job already advanced past pending/leased (running
		// or later) was already admitted by an earlier Advance call; only
		// JobStatePending (a fresh admission) and JobStateLeased (the
		// R-16.68c leased->running resume re-entry, HOW 3) are candidates
		// here. An unknown job (absent from jobStates) is treated as
		// pending -- the DAG planner's own newly-planned nodes have no
		// store row yet.
		if st, known := jobStates[n.ID]; known && st != JobStatePending && st != JobStateLeased {
			continue
		}
		if !depsAccepted(n, jobStates) {
			continue
		}
		scope, err := NormalizeScope(strings.Join(n.MutableScope, ","))
		if err != nil {
			delta.Errors = append(delta.Errors, err)
			continue
		}
		// A lease already held BY THIS NODE's OWN job (the leased-resume
		// case) never blocks its own re-admission; only another holder's
		// contending lease does.
		if intersectsContendingExcludingHolder(scope, activeLeases, n.ID) {
			continue
		}
		scopes[n.ID] = scope
		admissible = append(admissible, n)
	}
	return admissible, scopes
}

// depsAccepted reports whether every one of n's deps is currently
// JobStateAccepted -- an unknown dep (absent from jobStates) is treated
// as not-yet-accepted, fail-closed.
func depsAccepted(n DagNode, jobStates map[string]JobState) bool {
	for _, dep := range n.Deps {
		if jobStates[dep] != JobStateAccepted {
			return false
		}
	}
	return true
}

// intersectsContendingExcludingHolder reports whether scope intersects
// any active lease in a Contending() state -- held, renewing, or
// (R-21.139) expired_unconfirmed -- held by someone OTHER than
// selfHolder. A released or expired_orphaned lease never blocks; a
// lease already held by selfHolder itself never blocks its own
// re-admission (the R-16.68c leased->running resume case).
func intersectsContendingExcludingHolder(scope Scope, activeLeases []ResourceLease, selfHolder string) bool {
	for _, l := range activeLeases {
		if !l.State.Contending() || l.Holder == selfHolder {
			continue
		}
		leaseScope, err := ParseScope(l.ScopeGlob)
		if err != nil {
			continue // an unparseable stored scope cannot be intersected; skip rather than panic
		}
		if scope.Intersects(leaseScope) {
			return true
		}
	}
	return false
}

// observedFence returns the repo id and the epoch the scheduler observed
// for scope among activeLeases (0 if none is currently on record -- a
// brand-new scope's first acquire fences at epoch 0, matching
// lease_query.go's nextEpochTx starting sequence).
func observedFence(scope Scope, activeLeases []ResourceLease) (repoID string, epoch int64) {
	for _, l := range activeLeases {
		leaseScope, err := ParseScope(l.ScopeGlob)
		if err != nil {
			continue
		}
		if scope.Intersects(leaseScope) {
			return l.RepoID, l.Epoch
		}
	}
	return "", 0
}

// cancelPayloadHash derives a stable payload hash for the cancel-effect
// outbox intent from jobID alone -- the cancel effect carries no other
// caller-supplied payload, and R-21.148 requires the hash be stable
// (deterministic per input), not preimage-resistant.
func cancelPayloadHash(jobID string) string {
	sum := sha256.Sum256([]byte("cancel:" + jobID))
	return hex.EncodeToString(sum[:])
}
