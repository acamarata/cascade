// Purpose: Fleet, the single in-process door that turns a ResourceRequest into
// one lease on the reservation ledger (contract:fleet-lease). Lease
// resolves lanes and a tier through the Selector seam, then reserves through
// economics (the ONE ledger) and binds the placement, the execution row and
// the scheduler decision in one transaction.
//
// Inputs: FleetDeps (NewFleet), then per call a ResourceRequest, a DispatchRef
// and an attempt number.
// Outputs: a Lease, or a typed refusal (lease_errors.go).
// Constraints: built only by NewFleet; nothing is written before Reserve and
// every refusal before it writes zero rows; no reservation row is inserted
// outside economics; the lease is an in-process seam (no RPC). Every Fleet
// method takes ctx and uses the injected Clock; ids come from cascade.NewID.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"slices"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Fleet leases capacity. The zero value is not usable; construct with
// NewFleet.
type Fleet struct {
	deps  FleetDeps
	index leaseIndex
}

// NewFleet constructs a Fleet. Every FleetDeps field except Floors is
// required; a missing one refuses KindInvalidInput naming it.
func NewFleet(deps FleetDeps) (*Fleet, error) {
	if name := deps.missing(); name != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "capacity: NewFleet requires FleetDeps.%s", name)
	}
	deps.Floors = slices.Clone(deps.Floors)
	return &Fleet{deps: deps, index: newLeaseIndex()}, nil
}

// Lease resolves req into one lease. The order is: validate, snapshot,
// allowed lanes, sensitivity filter, Floors, Selector, placement, quota
// domain, then Reserve (execution id minted) and Bind (the execution row when
// d.JobID is set, and the scheduler decision) in one transaction.
func (f *Fleet) Lease(ctx context.Context, req ResourceRequest, d DispatchRef, attempt int) (Lease, error) {
	p, err := f.plan(ctx, req, d, attempt)
	if err != nil {
		return Lease{}, err
	}
	rsv, err := f.deps.Reserver.Reserve(ctx, reserveRequest(p))
	if err != nil {
		return Lease{}, refuse(ErrReservationFailed, err, "reserve lane %q", p.sel.Lane)
	}
	if rsv.State != economics.ReservationHeld {
		cause := cascade.Newf(cascade.KindConflict, "reservation %q is %s, not held", rsv.ID, rsv.State)
		return Lease{}, f.abandon(ctx, rsv.ID, refuse(ErrReservationFailed, cause, "a rate-limit cascade parked the reservation"))
	}
	bound, err := f.deps.Reserver.Bind(ctx, rsv.ID, placement(p), f.bindFn(p))
	if err != nil {
		return Lease{}, f.abandon(ctx, rsv.ID, refuse(ErrReservationFailed, err, "bind the placement of lane %q", p.sel.Lane))
	}
	l := f.newLease(p, bound)
	f.index.put(l.ID, leaseEntry{repoID: l.RepoID, executionID: l.ExecutionID, reservationID: l.ReservationID})
	return l, nil
}

// reserveRequest is the economics request for p. ScopeID is never set: the
// Reserver derives the limit scope from the quota domain.
func reserveRequest(p leasePlan) economics.ReserveRequest {
	return economics.ReserveRequest{
		ExecutionID: p.ids.execution, JobID: p.d.JobID, ProjectID: p.d.ProjectID, LaneID: string(p.sel.Lane),
		DomainID: p.domain, RepoID: p.d.RepoID, Kind: economics.ReservationInteractive,
		Estimate: p.estimate, ScopeGlobs: slices.Clone(p.d.ScopeGlobs),
	}
}

// placement is the Bind placement of p.
func placement(p leasePlan) economics.Placement {
	return economics.Placement{
		NodeID: p.node, SelectedTier: string(p.sel.Tier.Tier), Sensitivity: p.tier.String(), DecisionID: p.ids.decision,
	}
}

// newLease assembles the granted Lease from the plan and the bound row.
func (f *Fleet) newLease(p leasePlan, bound economics.Reservation) Lease {
	return Lease{
		ID: p.ids.lease, ReservationID: bound.ID, ExecutionID: p.ids.execution, DecisionID: p.ids.decision,
		SelectedTier: p.sel.Tier.Tier, LaneID: p.sel.Lane, NodeID: p.node, Sensitivity: p.tier,
		RepoID: p.d.RepoID, RepoLeaseIDs: slices.Clone(bound.LeaseIDs), Issued: f.deps.Clock.Now(), TTL: f.deps.TTL,
	}
}

// abandon retires a reservation Lease could not bind and returns cause, with
// any retirement failure appended to its detail. Bind rolls back its own
// failures; this covers a row Bind refused before its transaction and a row
// Reserve left parked. Release on a terminal row is a no-op.
func (f *Fleet) abandon(ctx context.Context, reservationID string, cause error) error {
	if _, err := f.deps.Reserver.Release(context.WithoutCancel(ctx), reservationID); err != nil {
		return refuse(ErrReservationFailed, err, "%v; the reservation could not be released", cause)
	}
	return cause
}
