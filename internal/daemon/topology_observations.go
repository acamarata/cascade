package daemon

// Purpose: steps 2, 4, 5 and 6 of the topology pass (ReconcileNow): agent
//
//	lanes with their changed-lane count; provider quota-window
//	observations become provider-status buckets; the WindowAccountant
//	fills estimate or unknown buckets only where no authoritative
//	(provider-status or cli-observation) bucket is stored; the file-only
//	lane reversal pins not yet recorded are applied.
//
// Inputs: the bound TopologyPassDeps, the runtime's one topology.Store and
// QuotaStore. Outputs: the count of buckets that really changed.
// Constraints: every write goes through QuotaStore.UpsertBucket and only
// when the stored bucket differs; a stale or unknown observation writes
// nothing; no row is ever deleted; time comes from the injected Clock.
//
// SPORT: internal/daemon (ADD, P1-TOP-06e).

import (
	"context"
	"reflect"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// observationKey is the provider key QuotaWindows is read under: the domain
// id for a subscription window (two subscriptions never share a row), else
// the account's provider. An api_project domain has no window buckets.
func observationKey(d topology.QuotaDomain, acct topology.Account) (string, bool) {
	switch d.Kind {
	case topology.QuotaDomainSubscriptionWindow:
		return string(d.ID), true
	case topology.QuotaDomainSharedPool:
		return acct.Provider, true
	case topology.QuotaDomainAPIProject:
		return "", false
	default:
		return "", false
	}
}

// observationDimension maps an observed window to the domain kind's
// dimension; any other window has no bucket.
func observationDimension(kind topology.QuotaDomainKind, w topology.BucketWindow) (string, bool) {
	sub := kind == topology.QuotaDomainSubscriptionWindow
	switch {
	case w == topology.BucketWindow5h && sub:
		return topology.DimensionSession5h, true
	case w == topology.BucketWindow5h:
		return topology.DimensionWindow5h, true
	case w == topology.BucketWindowWeek && sub:
		return topology.DimensionWeeklyShared, true
	case w == topology.BucketWindowWeek:
		return topology.DimensionWeekly, true
	}
	return "", false
}

// observationBuckets is step 4. A QuotaWindows error writes nothing for that
// domain and is returned, with the rows earlier domains changed.
func (r *FleetRuntime) observationBuckets(ctx context.Context, d TopologyPassDeps) (int, error) {
	domains, err := r.res.topo.ListQuotaDomains(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, dom := range domains {
		if dom.Kind == topology.QuotaDomainAPIProject {
			continue
		}
		acct, err := r.res.topo.GetAccount(ctx, dom.AccountRef)
		if err != nil {
			return changed, err
		}
		key, ok := observationKey(dom, acct)
		if !ok {
			continue
		}
		windows, err := d.QuotaWindows(ctx, key)
		if err != nil {
			return changed, err
		}
		for _, obs := range windows {
			n, err := r.applyObservation(ctx, dom, acct, obs)
			if err != nil {
				return changed, err
			}
			changed += n
		}
	}
	return changed, nil
}

// applyObservation writes one fresh, known observation over the stored
// bucket (its other fields kept) and reports 1 when the row changed.
func (r *FleetRuntime) applyObservation(ctx context.Context, dom topology.QuotaDomain, acct topology.Account, obs usage.QuotaObservation) (int, error) {
	dim, ok := observationDimension(dom.Kind, obs.Window)
	util := obs.Utilization()
	if !ok || obs.Stale || util < 0 {
		return 0, nil
	}
	stored, found, err := r.storedBucket(ctx, dom.ID, dim)
	if err != nil {
		return 0, err
	}
	next := stored
	if !found {
		next = topology.Bucket{Name: dim, Limit: topology.DiscoverLimit, Window: obs.Window,
			LimitScopeID: topology.ResolveScope(acct, dom), CapacityObserved: topology.UnobservedCapacity, Version: 1}
	}
	next.RemainingFraction = 1 - util/100
	next.ResetAt, next.ObservedAt = obs.ResetsAt, obs.ObservedAt
	next.Source, next.Confidence = topology.SourceProviderStatus, 1.0
	return r.upsertIfChanged(ctx, dom.ID, dim, stored, found, next)
}

// windowAccountantBuckets is step 5: for every subscription_window domain,
// the accountant's user-estimate or unknown buckets are written only where
// the stored bucket is not authoritative.
func (r *FleetRuntime) windowAccountantBuckets(ctx context.Context, d TopologyPassDeps) (int, error) {
	domains, err := r.res.topo.ListQuotaDomains(ctx)
	if err != nil {
		return 0, err
	}
	acct := economics.NewWindowAccountant(r.res.topo, r.res.quota, d.Usage, d.Clock)
	changed := 0
	for _, dom := range domains {
		if dom.Kind != topology.QuotaDomainSubscriptionWindow {
			continue
		}
		buckets, err := acct.Buckets(ctx, dom.ID)
		if err != nil {
			return changed, err
		}
		for _, b := range buckets {
			if b.Source != topology.SourceUserEstimate && b.Source != topology.SourceUnknown {
				continue
			}
			stored, found, err := r.storedBucket(ctx, dom.ID, b.Name)
			if err != nil {
				return changed, err
			}
			if found && authoritativeSource(stored.Source) {
				continue
			}
			n, err := r.upsertIfChanged(ctx, dom.ID, b.Name, stored, found, b)
			if err != nil {
				return changed, err
			}
			changed += n
		}
	}
	return changed, nil
}

// authoritativeSource reports a bucket source no estimate may overwrite.
func authoritativeSource(s topology.BucketSource) bool {
	return s == topology.SourceProviderStatus || s == topology.SourceCLIObservation
}

// storedBucket reads one bucket; a missing row is (zero, false, nil).
func (r *FleetRuntime) storedBucket(ctx context.Context, id topology.DomainID, dim string) (topology.Bucket, bool, error) {
	b, err := r.res.quota.GetBucket(ctx, id, dim)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return topology.Bucket{}, false, nil
	}
	if err != nil {
		return topology.Bucket{}, false, err
	}
	return b, true, nil
}

// upsertIfChanged writes next only when it differs from the stored bucket.
func (r *FleetRuntime) upsertIfChanged(ctx context.Context, id topology.DomainID, dim string, stored topology.Bucket, found bool, next topology.Bucket) (int, error) {
	if found && sameBucket(stored, next) {
		return 0, nil
	}
	if err := r.res.quota.UpsertBucket(ctx, id, dim, next); err != nil {
		return 0, err
	}
	return 1, nil
}

// sameBucket compares two buckets at the store's millisecond precision.
func sameBucket(a, b topology.Bucket) bool {
	sameTime := a.ResetAt.UnixMilli() == b.ResetAt.UnixMilli() && a.ObservedAt.UnixMilli() == b.ObservedAt.UnixMilli() &&
		a.ResetAt.IsZero() == b.ResetAt.IsZero() && a.ObservedAt.IsZero() == b.ObservedAt.IsZero()
	a.ResetAt, a.ObservedAt = b.ResetAt, b.ObservedAt
	return sameTime && a == b
}

// agentLanes is step 2; it counts the lanes it inserted or changed.
func (r *FleetRuntime) agentLanes(ctx context.Context, d TopologyPassDeps) (int, error) {
	before, err := r.res.topo.ListLanes(ctx, true)
	if err != nil {
		return 0, err
	}
	prior := make(map[topology.LaneID]topology.Lane, len(before))
	for _, l := range before {
		prior[l.ID] = l
	}
	if _, err := topology.ReconcileAgentLanes(ctx, r.res.topo, d.Clock, d.AgentLanes()); err != nil {
		return 0, err
	}
	after, err := r.res.topo.ListLanes(ctx, true)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, l := range after {
		if old, ok := prior[l.ID]; !ok || !reflect.DeepEqual(old, l) {
			changed++
		}
	}
	return changed, nil
}

// lanePins is step 6: each live pin not already recorded is applied.
func (r *FleetRuntime) lanePins(ctx context.Context, d TopologyPassDeps) (int, error) {
	applied := 0
	for _, pin := range r.topo.pins {
		if !pin.ReversalUntil.After(d.Clock.Now()) {
			continue
		}
		lane := topology.LaneID(pin.LaneID)
		rows, err := r.res.topo.ListReversals(ctx, lane)
		if err != nil {
			return applied, err
		}
		if pinRecorded(rows, pin) {
			continue
		}
		req := topology.ReversalRequest{Lane: lane, Class: pin.LaneClass, Until: pin.ReversalUntil, Reason: pin.ReversalReason}
		if err := r.res.topo.ReversePromotion(ctx, req, d.Clock, topologyAuditor{bus: d.Bus}); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// pinRecorded reports a reversal row with the pin's class and expiry.
func pinRecorded(rows []topology.Reversal, pin runtime.LaneReversalPin[topology.LaneClass]) bool {
	for _, row := range rows {
		if row.ReversedClass == pin.LaneClass && row.ExpiresAt.UnixMilli() == pin.ReversalUntil.UnixMilli() {
			return true
		}
	}
	return false
}
