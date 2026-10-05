// Purpose: step recovery and resume re-acquisition, both driven only by
//
//	what the ledger row persists (repo_id, job_id, scope_globs, lease
//	ids), never by an ephemeral ReserveRequest, so a Reserver built after
//	a restart can finish or undo what a dead process started.
//
// Inputs: a reservation id; the FindLeases/FindWorktree seams (recovery)
//
//	and the ValidateLeases/ReleaseLeases/AcquireLeases seams (resume).
//
// Outputs: RecoverSteps, ReacquireForResume.
// Constraints: a lookup error means "unknown", never "not run": the row
//
//	and its pending step are left exactly as stored and the error is
//	returned. A found handle is recorded (acquired) so the teardown that
//	follows compensates it; a not-found result marks the step not_run.
//	Resume never releases or adopts a lease the row does not store.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RecoverSteps resolves every pending step of id's ledger by re-querying
// the subsystems with the row's persisted inputs, then persists the
// resolved ledger. It changes no state and is safe to repeat.
func (rv *Reserver) RecoverSteps(ctx context.Context, id string) (Reservation, error) {
	r, err := rv.mustGet(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.State.Terminal() {
		return r, nil
	}
	resolved, changed, err := rv.resolvePending(ctx, r)
	if err != nil {
		return r, err
	}
	if !changed {
		return r, nil
	}
	if err := rv.store.Replace(ctx, resolved); err != nil {
		return r, err
	}
	return resolved, nil
}

// resolvePending returns r with every pending step resolved, or the
// first lookup error (r itself is never partially resolved).
func (rv *Reserver) resolvePending(ctx context.Context, r Reservation) (Reservation, bool, error) {
	changed := false
	for _, s := range append([]Step(nil), r.Steps...) {
		if s.State != StepPending {
			continue
		}
		changed = true
		var err error
		switch s.Step {
		case StepLeases:
			r, err = rv.recoverLeases(ctx, r)
		case StepWorktree:
			r, err = rv.recoverWorktree(ctx, r)
		case StepQuota:
			r = setStep(r, StepQuota, StepNotRun, "") // no external effect; the row is the quota
		}
		if err != nil {
			return Reservation{}, false, err
		}
	}
	return r, changed, nil
}

// recoverLeases records every live lease the job holds in the stored
// repo on the stored globs, or marks the step not_run when there is none.
func (rv *Reserver) recoverLeases(ctx context.Context, r Reservation) (Reservation, error) {
	ids, err := rv.seams.FindLeases(ctx, r.RepoID, r.JobID, r.ScopeGlobs)
	if err != nil {
		return Reservation{}, cascade.Wrapf(cascade.KindUnavailable, err, "economics: recover leases of %q", r.ID)
	}
	if len(ids) == 0 {
		return setStep(r, StepLeases, StepNotRun, ""), nil
	}
	r.LeaseIDs = ids
	return setStep(r, StepLeases, StepAcquired, strings.Join(ids, ",")), nil
}

// recoverWorktree records the worktree allocated for the row's first
// lease, or marks the step not_run when there is none.
func (rv *Reserver) recoverWorktree(ctx context.Context, r Reservation) (Reservation, error) {
	path, found, err := rv.seams.FindWorktree(ctx, primaryLease(r.LeaseIDs))
	if err != nil {
		return Reservation{}, cascade.Wrapf(cascade.KindUnavailable, err, "economics: recover worktree of %q", r.ID)
	}
	if !found {
		return setStep(r, StepWorktree, StepNotRun, ""), nil
	}
	r.WorktreeID = path
	return setStep(r, StepWorktree, StepAcquired, path), nil
}

// ReacquireForResume revalidates the row's stored leases after a
// restart. Every stored lease still valid: kept, nothing called. Any
// invalid: the stored leases still valid are released and the full
// stored scope is re-acquired in one AcquireLeases call with the stored
// repo, job and globs; a failed re-acquisition rolls the row back.
func (rv *Reserver) ReacquireForResume(ctx context.Context, id string) (Reservation, error) {
	r, err := rv.mustGet(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.State.Terminal() {
		return r, cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "cannot resume %q: it is %s", id, r.State)
	}
	if len(r.ScopeGlobs) == 0 {
		return r, nil
	}
	valid, err := rv.seams.ValidateLeases(ctx, r.JobID, r.LeaseIDs)
	if err != nil {
		return r, err
	}
	stillValid := intersect(r.LeaseIDs, valid)
	if len(r.LeaseIDs) > 0 && len(stillValid) == len(r.LeaseIDs) {
		return r, nil
	}
	r.LeaseIDs = stillValid
	if len(stillValid) > 0 {
		if err := rv.seams.ReleaseLeases(ctx, stillValid); err != nil {
			return rv.failAndRollback(ctx, r, err)
		}
		r.LeaseIDs = nil
	}
	ids, err := rv.seams.AcquireLeases(ctx, r.RepoID, r.JobID, r.ScopeGlobs)
	if err == nil && len(ids) == 0 {
		err = cascade.Newf(cascade.KindInternal, "economics: lease re-acquisition for %v returned no lease id", r.ScopeGlobs)
	}
	if err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	r.LeaseIDs = ids
	r = setStep(r, StepLeases, StepAcquired, strings.Join(ids, ","))
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	return r, nil
}

// intersect returns the ids of stored that also appear in valid, in
// stored order: a validator can never add a lease the row does not hold.
func intersect(stored, valid []string) []string {
	ok := make(map[string]bool, len(valid))
	for _, v := range valid {
		ok[v] = true
	}
	var out []string
	for _, s := range stored {
		if ok[s] {
			out = append(out, s)
		}
	}
	return out
}
