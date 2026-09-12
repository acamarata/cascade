// Purpose: QuotaSnapshot/DomainQuota assembly and TakeQuotaSnapshot, the
//
//	fleet.quota.snapshot RPC's payload builder. A domain's confidence is
//	the MINIMUM confidence across its dimensions (an unobserved dimension
//	contributes 0), so a domain is never presented as better known than
//	its weakest bucket.
//
// Inputs: a *QuotaStore and the domains to report on. Outputs: a
//
//	QuotaSnapshot, appended to sessions_quota_snapshot as a side effect.
//
// Constraints: TakeQuotaSnapshot reads the CURRENT bucket rows for every domain in
//
//	one pass before assembling the result, so no snapshot observes a
//	half-applied set of bucket writes (this package's consistency
//	boundary -- see this file's Snapshot doc comment).
//
// SPORT: fleet/topology/quota_snapshot/ADD (P1-E40-W9-S77-T3).

package topology

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DomainQuota is one domain's row of a QuotaSnapshot.
type DomainQuota struct {
	DomainID    DomainID          `json:"domain_id"`
	Kind        QuotaDomainKind   `json:"kind"`
	BillingTier BillingTier       `json:"billing_tier"`
	Quarantined bool              `json:"quarantined"`
	Dimensions  map[string]Bucket `json:"dimensions"`
	Batch       BatchLimits       `json:"batch"`
	// Confidence is the MINIMUM confidence across Dimensions; an
	// unobserved dimension (absent from Dimensions) contributes 0. A
	// domain with zero dimensions reports confidence 0, never a default
	// "fully known" reading.
	Confidence float64 `json:"confidence"`
}

// QuotaSnapshot is the fleet.quota.snapshot RPC's full payload.
type QuotaSnapshot struct {
	TakenAt time.Time     `json:"taken_at"`
	Domains []DomainQuota `json:"domains"`
}

// domainConfidence computes one domain's minimum-confidence rule over its
// expected dimension set (per the closed per-kind set), so a dimension the
// domain has never observed (absent from dims) contributes 0 rather than
// being skipped.
func domainConfidence(kind QuotaDomainKind, dims map[string]Bucket) float64 {
	names := dimensionSets[kind]
	if len(names) == 0 {
		return 0
	}
	minConfidence := 1.0
	for name := range names {
		b, ok := dims[name]
		if !ok {
			return 0
		}
		if b.Confidence < minConfidence {
			minConfidence = b.Confidence
		}
	}
	return minConfidence
}

// buildDomainQuota assembles one DomainQuota from a QuotaDomain and its
// freshly-read bucket rows, FAIL CLOSED against dimensions.go's per-kind
// closed set: a stored bucket whose dimension name does not belong to d's
// Kind is refused (ErrTopologyInvariant) rather than silently reported,
// which would let a corrupt or cross-migrated row read as a real,
// well-known dimension of the wrong kind.
func buildDomainQuota(d QuotaDomain, dims map[string]Bucket) (DomainQuota, error) {
	if err := ValidateDimensions(d.Kind, dims); err != nil {
		return DomainQuota{}, err
	}
	return DomainQuota{
		DomainID: d.ID, Kind: d.Kind, BillingTier: d.BillingTier, Quarantined: d.Quarantined,
		Dimensions: dims, Batch: d.Batch, Confidence: domainConfidence(d.Kind, dims),
	}, nil
}

// idMinter mints a fresh identifier for one snapshot row. Duck-typed so
// tests can inject a deterministic minter instead of cascade.NewID's real
// crypto/rand source (Art.7.3).
type idMinter func() (string, error)

// realIDMinter is TakeQuotaSnapshot's production idMinter.
func realIDMinter() (string, error) {
	id, err := cascade.NewID()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// TakeQuotaSnapshot reads the CURRENT sessions_quota_bucket rows for every
// domain in domains (one ListBuckets call per domain, all inside this
// single call so no writer's UpsertBucket call can be observed
// half-applied across two domains of the same snapshot), assembles a
// QuotaSnapshot at now, appends it to sessions_quota_snapshot, and returns
// it. Named TakeQuotaSnapshot rather than the contract's literal
// "Snapshot(ctx)" because this package's own invariants.go already
// declares an exported type Snapshot (the invariant-checker's row set,
// P1-E40-W9-S77-T1) -- a same-named function would not compile
// (R-16.79: follow the tree over contract text).
func TakeQuotaSnapshot(ctx context.Context, store *QuotaStore, domains []QuotaDomain, now time.Time) (QuotaSnapshot, error) {
	return snapshotWithMinter(ctx, store, domains, now, realIDMinter)
}

func snapshotWithMinter(ctx context.Context, store *QuotaStore, domains []QuotaDomain, now time.Time, mint idMinter) (QuotaSnapshot, error) {
	if store == nil {
		return QuotaSnapshot{}, cascade.New(cascade.KindUnavailable, "topology: TakeQuotaSnapshot requires a non-nil QuotaStore")
	}
	snap := QuotaSnapshot{TakenAt: now, Domains: make([]DomainQuota, 0, len(domains))}
	for _, d := range domains {
		buckets, err := store.ListBuckets(ctx, d.ID)
		if err != nil {
			return QuotaSnapshot{}, err
		}
		dims := make(map[string]Bucket, len(buckets))
		for _, b := range buckets {
			dims[b.Name] = b
		}
		dq, err := buildDomainQuota(d, dims)
		if err != nil {
			return QuotaSnapshot{}, err
		}
		snap.Domains = append(snap.Domains, dq)
	}
	id, err := mint()
	if err != nil {
		return QuotaSnapshot{}, cascade.Wrap(cascade.KindInternal, err, "topology: mint quota snapshot id")
	}
	if err := store.AppendSnapshot(ctx, id, snap); err != nil {
		return QuotaSnapshot{}, err
	}
	return snap, nil
}
