// Purpose: the R-21.35 (as amended by R-21.114/R-21.122) atomic
//
//	reservation pipeline: NewReserver's nil-seam refusal, and Reserve's
//	ordered quota -> leases -> worktree acquisition chain with the held
//	row persisted first so a crash at any point leaves exactly one
//	fenced, sweepable row. reserve_teardown.go holds the reverse
//	rollback chain, Commit and Release (300-line cap).
//
// Inputs: a ReservationStore, an injected Clock and the seven func-typed
//
//	seams reserve_types.go declares.
//
// Outputs: NewReserver, Reserve.
// Constraints: the M/S-26.T2 permit is NOT acquired inside Reserve
//
//	(R-21.122); admission is serialized per Reserver instance via an
//	in-process mutex around persist+check, documented in this ticket's
//	journal as a deliberate single-process simplification of the
//	contract's literal "ONE BEGIN IMMEDIATE transaction" wording (see
//	the journal's CONTRADICTION note).
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"context"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Reserver drives the R-21.35 reservation pipeline. The zero value is
// not usable; construct with NewReserver.
type Reserver struct {
	store            *ReservationStore
	clock            Clock
	buckets          DomainBucketsFn
	permit           PermitFn
	acquireLeases    AcquireLeasesFn
	releaseLeases    ReleaseLeasesFn
	allocateWorktree AllocateWorktreeFn
	removeWorktree   RemoveWorktreeFn
	validateLeases   ValidateLeasesFn
	ownerEpoch       string

	// mu serializes persist-then-check across concurrent Reserve calls
	// on this Reserver, so two racing callers can never both read the
	// same pre-decision outstanding total.
	mu sync.Mutex
}

// NewReserver constructs a Reserver. Every seam is required; a nil
// argument is a construction-time typed error, never a default-true
// no-op (Art.1).
func NewReserver(store *ReservationStore, clock Clock, ownerEpoch string,
	buckets DomainBucketsFn, permit PermitFn, acquireLeases AcquireLeasesFn,
	releaseLeases ReleaseLeasesFn, allocateWorktree AllocateWorktreeFn,
	removeWorktree RemoveWorktreeFn, validateLeases ValidateLeasesFn) (*Reserver, error) {
	switch {
	case store == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil store")
	case clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil Clock")
	case buckets == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil DomainBucketsFn")
	case permit == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil PermitFn")
	case acquireLeases == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil AcquireLeasesFn")
	case releaseLeases == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil ReleaseLeasesFn")
	case allocateWorktree == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil AllocateWorktreeFn")
	case removeWorktree == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil RemoveWorktreeFn")
	case validateLeases == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil ValidateLeasesFn")
	case ownerEpoch == "":
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-empty ownerEpoch")
	}
	return &Reserver{
		store: store, clock: clock, ownerEpoch: ownerEpoch,
		buckets: buckets, permit: permit, acquireLeases: acquireLeases,
		releaseLeases: releaseLeases, allocateWorktree: allocateWorktree,
		removeWorktree: removeWorktree, validateLeases: validateLeases,
	}, nil
}

// validate checks req's required identity fields and the R-21.82 rule
// that a batch reservation never carries ScopeGlobs.
func validateReserveRequest(req ReserveRequest) error {
	for name, v := range map[string]string{"JobID": req.JobID, "ProjectID": req.ProjectID, "LaneID": req.LaneID, "DomainID": req.DomainID, "ScopeID": req.ScopeID} {
		if v == "" {
			return cascade.Newf(cascade.KindInvalidInput, "economics: ReserveRequest.%s is required", name)
		}
	}
	if req.Kind == ReservationBatch && len(req.ScopeGlobs) > 0 {
		return cascade.New(cascade.KindInvalidInput, "economics: a batch reservation must not carry ScopeGlobs")
	}
	return nil
}

// batchExpiry is R-21.82's separate long expiry for a batch reservation.
const batchExpiry = 24 * 60 * 60 // seconds

// Reserve executes the R-21.35 ordered pipeline: persist a held row
// first, then admit against derived availability, then (for kind
// interactive only -- R-21.82 skips both for batch) acquire the leases
// and the worktree. Any failure runs the exact reverse teardown and
// returns the row marked rolled_back alongside the triggering error.
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
	now := rv.clock.Now()
	r := Reservation{
		ID: id.String(), JobID: req.JobID, ProjectID: req.ProjectID, LaneID: req.LaneID,
		DomainID: req.DomainID, ScopeID: req.ScopeID, Kind: req.Kind, Estimate: req.Estimate,
		ActualSource: ActualSourceEstimated, BasePrice: req.BasePrice, State: ReservationHeld,
		OwnerEpoch: rv.ownerEpoch, HeartbeatAt: now.Unix(),
	}
	if req.Kind == ReservationBatch {
		r.ExpiresAt = now.Unix() + batchExpiry
	}
	r = appendStep(r, StepQuota, r.ID)

	rv.mu.Lock()
	r, err = rv.store.Insert(ctx, r)
	if err != nil {
		rv.mu.Unlock()
		return Reservation{}, err
	}
	admitErr := rv.checkAdmission(ctx, r)
	if admitErr == nil {
		r = markStepAcquired(r, StepQuota, "")
		if repErr := rv.store.Replace(ctx, r); repErr != nil {
			admitErr = repErr
		}
	}
	rv.mu.Unlock()
	if admitErr != nil {
		return rv.failAndRollback(ctx, r, admitErr)
	}

	if req.Kind == ReservationBatch {
		return r, nil
	}
	return rv.acquireInteractive(ctx, r, req)
}

// checkAdmission refuses with ErrQuotaUnavailable when r's own combined
// estimate weight would exceed the domain's derived availability:
// capacity_observed - committed_since_observation, summed across every
// dimension bucket, minus the outstanding estimates of every OTHER
// held/parked/committed reservation on the same domain (R-21.114 -- no
// bucket counter is mutated by this read).
func (rv *Reserver) checkAdmission(ctx context.Context, r Reservation) error {
	buckets, err := rv.buckets(ctx, r.DomainID)
	if err != nil {
		return err
	}
	var capacity, committed int64
	for _, b := range buckets {
		capacity += b.CapacityObserved
		committed += b.CommittedSinceObservation
	}
	outstanding, err := rv.store.outstandingHeldExcept(ctx, r.DomainID, r.ID)
	if err != nil {
		return err
	}
	requested := r.Estimate.TokensIn + r.Estimate.TokensOut + r.Estimate.Requests
	available := capacity - committed - outstanding
	if requested > available {
		return cascade.Wrapf(cascade.KindUnavailable, ErrQuotaUnavailable, "domain %q: requested %d > available %d", r.DomainID, requested, available)
	}
	return nil
}

// acquireInteractive runs the leases-then-worktree steps for an
// interactive reservation, rolling back on either failure.
func (rv *Reserver) acquireInteractive(ctx context.Context, r Reservation, req ReserveRequest) (Reservation, error) {
	r = appendStep(r, StepLeases, r.ID)
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	leaseIDs, err := rv.acquireLeases(ctx, req.JobID, req.ScopeGlobs)
	if err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	r.LeaseIDs = leaseIDs
	r = markStepAcquired(r, StepLeases, strings.Join(leaseIDs, ","))
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}

	r = appendStep(r, StepWorktree, r.ID)
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	var primaryLease string
	if len(leaseIDs) > 0 {
		primaryLease = leaseIDs[0]
	}
	worktreeID, err := rv.allocateWorktree(ctx, primaryLease)
	if err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	r.WorktreeID = worktreeID
	r = markStepAcquired(r, StepWorktree, worktreeID)
	if err := rv.store.Replace(ctx, r); err != nil {
		return rv.failAndRollback(ctx, r, err)
	}
	return r, nil
}

// appendStep appends a new pending Step for name, keyed by the
// reservation-derived idempotency key `<reservation id>:<step>`,
// written BEFORE the acquisition it describes (R-21.97).
func appendStep(r Reservation, name StepName, reservationID string) Reservation {
	r.Steps = append(r.Steps, Step{Step: name, IdempotencyKey: reservationID + ":" + string(name), State: StepPending})
	return r
}

// markStepAcquired marks the most recently appended Step for name
// acquired, recording handle.
func markStepAcquired(r Reservation, name StepName, handle string) Reservation {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Step == name {
			r.Steps[i].State = StepAcquired
			r.Steps[i].Handle = handle
			break
		}
	}
	return r
}
