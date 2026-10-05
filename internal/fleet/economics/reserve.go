// Purpose: the R-21.35 (as amended by R-21.114/R-21.122) atomic
//
//	reservation pipeline: Reserve's ordered quota -> leases -> worktree
//	acquisition chain with the held row persisted first, so a crash at
//	any point leaves exactly one fenced, sweepable row whose step ledger
//	says how far it got. reserve_teardown.go holds the reverse rollback
//	chain, Commit and Release.
//
// Inputs: a Reserver (reserve_ledger.go) and a ReserveRequest.
// Outputs: Reserve.
// Constraints: the permit is NOT acquired inside Reserve; the held
//
//	insert, the availability check and the share check run under the
//	Reserver's mutex, so every Reserve on the daemon's one Reserver is
//	serialized. A second daemon on the same home is refused by Sweep
//	(ErrConcurrentDaemon) rather than serialized across processes.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// validateReserveRequest checks req's required identity fields. A lease
// needs a job and a repo, so JobID and RepoID are required only when
// ScopeGlobs is non-empty, and a batch reservation carries no globs.
func validateReserveRequest(req ReserveRequest) error {
	required := [][2]string{{"ExecutionID", req.ExecutionID}, {"ProjectID", req.ProjectID}, {"LaneID", req.LaneID}, {"DomainID", req.DomainID}}
	if len(req.ScopeGlobs) > 0 {
		required = append(required, [2]string{"JobID", req.JobID}, [2]string{"RepoID", req.RepoID})
	}
	for _, f := range required {
		if f[1] == "" {
			return cascade.Newf(cascade.KindInvalidInput, "economics: ReserveRequest.%s is required", f[0])
		}
	}
	if req.Kind == ReservationBatch && len(req.ScopeGlobs) > 0 {
		return cascade.New(cascade.KindInvalidInput, "economics: a batch reservation must not carry ScopeGlobs")
	}
	return nil
}

// batchExpiry is the separate long expiry of a batch reservation.
const batchExpiry = 24 * 60 * 60 // seconds

// Reserve persists a held row first, then admits it per dimension and
// against the project share, then (for an interactive request with
// ScopeGlobs) acquires the leases and the worktree. Any failure runs the
// exact reverse teardown and returns the row marked rolled_back with the
// triggering error. A cascade that lands during the acquisition parks the
// row once it finishes, so the row returned may be parked.
func (rv *Reserver) Reserve(ctx context.Context, req ReserveRequest) (Reservation, error) {
	if req.Kind == "" {
		req.Kind = ReservationInteractive
	}
	if !req.Kind.Valid() {
		return Reservation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationKind, "%q", string(req.Kind))
	}
	if err := validateReserveRequest(req); err != nil {
		return Reservation{}, err
	}
	id, err := cascade.NewID()
	if err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindInternal, err, "economics: mint reservation id")
	}
	now := rv.clock.Now().Unix()
	r := Reservation{
		ID: id.String(), ExecutionID: req.ExecutionID, JobID: req.JobID, ProjectID: req.ProjectID, LaneID: req.LaneID,
		DomainID: req.DomainID, RepoID: req.RepoID, ScopeGlobs: append([]string(nil), req.ScopeGlobs...), Kind: req.Kind,
		Estimate: req.Estimate, ActualSource: ActualSourceEstimated, BasePrice: req.BasePrice, State: ReservationHeld,
		OwnerEpoch: rv.ownerEpoch, HeartbeatAt: now,
	}
	if req.Kind == ReservationBatch {
		r.ExpiresAt = now + batchExpiry
	}
	rid := r.ID
	rv.beginAcquire(rid) // before the held row is visible to a cascade
	defer rv.endAcquire(ctx, rid)
	r, err = rv.admit(ctx, appendStep(r, StepQuota), req.ScopeID)
	if err == nil && len(r.ScopeGlobs) > 0 {
		r, err = rv.acquireInteractive(ctx, r)
	}
	return rv.finishAcquire(ctx, rid, r, err)
}

// admit runs admitLocked under rv.mu, released by a defer so a panicking
// seam cannot leave every later Reserve blocked.
func (rv *Reserver) admit(ctx context.Context, r Reservation, callerScope string) (Reservation, error) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return rv.admitLocked(ctx, r, callerScope)
}

// admitLocked inserts the held row, then runs the availability and share
// checks against it. The caller holds rv.mu across the whole call, so no
// other Reserve reads the outstanding total between the insert and the
// decision. A refused row is rolled back before the lock is released.
func (rv *Reserver) admitLocked(ctx context.Context, r Reservation, callerScope string) (Reservation, error) {
	r, err := rv.store.Insert(ctx, r)
	if err != nil {
		return Reservation{}, err
	}
	rv.track(r.ID)
	admitErr := rv.checkAdmission(ctx, &r, callerScope)
	if admitErr == nil {
		r = setStep(r, StepQuota, StepAcquired, "")
		admitErr = rv.store.Replace(ctx, r)
	}
	if admitErr != nil {
		return rv.failAndRollback(ctx, r, admitErr)
	}
	return r, nil
}

// acquireInteractive runs the leases step then the worktree step, each
// recorded pending before its call and acquired after it, rolling back
// on any failure.
func (rv *Reserver) acquireInteractive(ctx context.Context, r Reservation) (Reservation, error) {
	r = appendStep(r, StepLeases)
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	leaseIDs, err := rv.seams.AcquireLeases(ctx, r.RepoID, r.JobID, r.ScopeGlobs)
	if err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	r.LeaseIDs = leaseIDs
	if len(leaseIDs) == 0 {
		return rv.failAndRollback(ctx, r, cascade.Newf(cascade.KindInternal, "economics: lease acquisition for %v returned no lease id", r.ScopeGlobs))
	}
	r = setStep(r, StepLeases, StepAcquired, strings.Join(leaseIDs, ","))
	r = appendStep(r, StepWorktree)
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	worktreeID, err := rv.seams.AllocateWorktree(ctx, primaryLease(leaseIDs))
	if err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	r.WorktreeID = worktreeID
	r = setStep(r, StepWorktree, StepAcquired, worktreeID)
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	return r, nil
}

// primaryLease is the lease a worktree is bound to: the first one.
func primaryLease(leaseIDs []string) string {
	if len(leaseIDs) == 0 {
		return ""
	}
	return leaseIDs[0]
}
