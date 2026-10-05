// Purpose: per-dimension admission and the project share. Availability
//
//	is derived, never counted down: for every bucket of the domain,
//	topology.Available nets the observed capacity against every held,
//	parked and committed reservation on the same limit scope (across
//	domains), per dimension name, using the ReservationUnits table.
//
// Inputs: the Buckets seam (domain kind, its limit scope, its buckets),
//
//	the active rows on that scope, the ReserveFraction seam and the live
//	ActiveProjectCount.
//
// Outputs: nil, or ErrQuotaUnavailable / ErrProjectShareExceeded naming
//
//	the dimension, bucket, kind or project that refused.
//
// Constraints: fails closed -- an unknown kind, a domain with no buckets,
//
//	a unit dimension of the kind with no bucket, a missing barrier
//	bucket, a bucket on another scope, a bucket without a unit row, and
//	Limit 0 all refuse. Only an undiscovered bucket (DiscoverLimit and
//	UnobservedCapacity) skips the capacity gate. Buckets are only read.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// quotaErr wraps ErrQuotaUnavailable with a message naming the cause.
func quotaErr(format string, args ...any) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrQuotaUnavailable, format, args...)
}

// checkAdmission resolves r's domain through the Buckets seam, sets
// r.ScopeID to the domain's scope, and admits r only when every
// dimension and the project share can absorb its estimate. The caller
// holds rv.mu.
func (rv *Reserver) checkAdmission(ctx context.Context, r *Reservation, callerScope string) error {
	kind, scope, buckets, err := rv.seams.Buckets(ctx, r.DomainID)
	if err != nil {
		return err
	}
	if callerScope != "" && callerScope != string(scope) {
		return cascade.Newf(cascade.KindInvalidInput, "economics: ReserveRequest.ScopeID %q differs from domain %q scope %q", callerScope, r.DomainID, scope)
	}
	r.ScopeID = string(scope)
	if len(buckets) == 0 {
		return quotaErr("domain %q has no buckets", r.DomainID)
	}
	others, err := rv.store.activeOnScope(ctx, r.ScopeID, r.ID)
	if err != nil {
		return err
	}
	now := rv.clock.Now()
	avail, err := dimensionAvailability(kind, scope, buckets, others, now)
	if err != nil {
		return err
	}
	if err := checkDimensions(kind, buckets, r.Estimate, avail); err != nil {
		return err
	}
	return rv.checkProjectShare(ctx, *r, kind, buckets, others, now)
}

// sortedDims returns buckets' dimension names in order, so a refusal
// names the same dimension on every run.
func sortedDims(buckets map[string]topology.Bucket) []string {
	out := make([]string, 0, len(buckets))
	for dim := range buckets {
		out = append(out, dim)
	}
	sort.Strings(out)
	return out
}

// undiscovered reports the one bucket shape that skips the capacity
// gate: no limit known and no capacity ever observed.
func undiscovered(b topology.Bucket) bool {
	return b.Limit == topology.DiscoverLimit && b.CapacityObserved == topology.UnobservedCapacity
}

// capacityOf is the observed capacity, or Limit when none was observed.
func capacityOf(b topology.Bucket) int64 {
	if b.CapacityObserved != topology.UnobservedCapacity {
		return b.CapacityObserved
	}
	return b.Limit
}

// estimatesOn projects the active rows onto one dimension's units.
func estimatesOn(others []Reservation, dim string, fn UnitFn) []topology.ReservationEstimate {
	out := make([]topology.ReservationEstimate, 0, len(others))
	for _, o := range others {
		out = append(out, topology.ReservationEstimate{
			ReservationID: o.ID, LimitScopeID: topology.LimitScopeID(o.ScopeID), Dimension: dim,
			State: topology.ReservationState(o.State), Estimate: fn(o.Estimate),
		})
	}
	return out
}

// requireEveryDimension refuses when a dimension the kind reserves
// against has no bucket: admitting against only the buckets that exist
// would let an unseen dimension absorb any amount.
func requireEveryDimension(kind topology.QuotaDomainKind, units map[string]UnitFn, buckets map[string]topology.Bucket) error {
	dims := make([]string, 0, len(units))
	for dim := range units {
		dims = append(dims, dim)
	}
	sort.Strings(dims)
	for _, dim := range dims {
		if _, ok := buckets[dim]; !ok {
			return quotaErr("%s dimension %q has no bucket", kind, dim)
		}
	}
	return nil
}

// dimensionAvailability returns topology.Available for every
// capacity-gated bucket, net of others. An unknown kind, a unit
// dimension without a bucket, a bucket on another scope, or a bucket
// without a ReservationUnits row refuses.
func dimensionAvailability(kind topology.QuotaDomainKind, scope topology.LimitScopeID, buckets map[string]topology.Bucket, others []Reservation, now time.Time) (map[string]int64, error) {
	units, ok := ReservationUnits[kind]
	if !ok || !kind.Valid() {
		return nil, quotaErr("unknown domain kind %q: no reservation units", kind)
	}
	if err := requireEveryDimension(kind, units, buckets); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(buckets))
	for _, dim := range sortedDims(buckets) {
		b := buckets[dim]
		if b.LimitScopeID != scope {
			return nil, quotaErr("bucket %q carries scope %q, not the domain scope %q", dim, b.LimitScopeID, scope)
		}
		fn, ok := units[dim]
		if !ok {
			return nil, quotaErr("bucket %q: no reservation unit for %s dimension %q", dim, kind, dim)
		}
		if undiscovered(b) {
			continue
		}
		out[dim] = topology.Available(scope, dim, capacityOf(b), b.CommittedSinceObservation, estimatesOn(others, dim, fn), now)
	}
	return out, nil
}

// checkDimensions refuses the first dimension that cannot take est: a
// bucket topology.Reservable refuses (Limit 0, or a request above a known
// limit), or a gated dimension whose availability is below the request.
func checkDimensions(kind topology.QuotaDomainKind, buckets map[string]topology.Bucket, est Estimate, avail map[string]int64) error {
	units := ReservationUnits[kind]
	for _, dim := range sortedDims(buckets) {
		b := buckets[dim]
		need := units[dim](est)
		if !topology.Reservable(b, need) {
			return quotaErr("dimension %q: requested %d is not reservable against limit %d", dim, need, b.Limit)
		}
		if a, gated := avail[dim]; gated && need > a {
			return quotaErr("dimension %q: requested %d > available %d", dim, need, a)
		}
	}
	return nil
}

// checkProjectShare refuses when r's project would hold more than its
// share of the barrier bucket: share = ReserveAdjustedRemaining(barrier,
// reserve) / max(1, active projects), in units of barrier capacity. An
// absent barrier bucket refuses; only an undiscovered or unknown-capacity
// barrier skips the rule.
func (rv *Reserver) checkProjectShare(ctx context.Context, r Reservation, kind topology.QuotaDomainKind, buckets map[string]topology.Bucket, others []Reservation, now time.Time) error {
	dim, err := topology.BarrierBucket(kind)
	if err != nil {
		return quotaErr("domain %q: %v", r.DomainID, err)
	}
	b, ok := buckets[dim]
	if !ok {
		return quotaErr("domain %q: barrier dimension %q has no bucket", r.DomainID, dim)
	}
	if undiscovered(b) || capacityOf(b) < 0 {
		return nil
	}
	reserve, err := rv.seams.ReserveFraction(ctx, r.DomainID)
	if err != nil {
		return err
	}
	b.RemainingFraction = topology.RemainingFraction(b, now)
	projects := max(1, rv.seams.Projects.Count())
	limit := ReserveAdjustedRemaining(b, reserve) / float64(projects) * float64(capacityOf(b))
	fn := ReservationUnits[kind][dim]
	held := fn(r.Estimate)
	for _, o := range others {
		if o.ProjectID == r.ProjectID {
			held += fn(o.Estimate)
		}
	}
	if float64(held) > limit {
		return cascade.Wrapf(cascade.KindQuotaExhausted, ErrProjectShareExceeded,
			"project %q: %d %s units would exceed its share %.2f (%d active projects)", r.ProjectID, held, dim, limit, projects)
	}
	return nil
}
