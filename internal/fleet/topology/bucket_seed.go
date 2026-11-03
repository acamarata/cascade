// Purpose: SeedUndiscoveredBuckets gives each domain with zero buckets
// its closed dimension set at DiscoverLimit/UnobservedCapacity, so a
// fresh domain is reservable before any provider observation arrives.
//
// Inputs: a *QuotaStore (the only bucket writer) and a *Store (for
// listing domains). Outputs: the count of buckets seeded, or a
// *cascade.Error.
//
// Constraints: a domain with any existing bucket is skipped entirely;
// an unknown domain kind reports a violation and writes nothing; no
// provider network call is made.
//
// SPORT: fleet/topology/bucket_seed/ADD (P1-E40-W9-S77-T3).

package topology

import (
	"context"
	"sort"
)

// SeedUndiscoveredBuckets seeds each domain with zero buckets with its
// closed dimension set at DiscoverLimit/UnobservedCapacity. Returns the
// count of buckets seeded. A domain with any existing bucket is skipped;
// an unknown domain kind writes nothing and reports a violation.
func SeedUndiscoveredBuckets(ctx context.Context, q *QuotaStore, s *Store) (int, error) {
	domains, err := s.ListQuotaDomains(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, d := range domains {
		buckets, err := q.ListBuckets(ctx, d.ID)
		if err != nil {
			return count, err
		}
		if len(buckets) > 0 {
			continue
		}

		dims, ok := dimensionSets[d.Kind]
		if !ok {
			return count, newInvariantErr("quota_domain_kind", string(d.ID), "unknown domain kind")
		}

		account, err := s.GetAccount(ctx, d.AccountRef)
		if err != nil {
			return count, err
		}
		scopeID := ResolveScope(account, d)
		names := make([]string, 0, len(dims))
		for name := range dims {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, dimName := range names {
			b := undiscoveredBucket(dimName, d.Kind, scopeID)
			if err := q.UpsertBucket(ctx, d.ID, dimName, b); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// undiscoveredBucket returns a bucket for dimName at DiscoverLimit and
// UnobservedCapacity with SourceUnknown.
func undiscoveredBucket(dimName string, kind QuotaDomainKind, scopeID LimitScopeID) Bucket {
	return Bucket{
		Name:                      dimName,
		Limit:                     DiscoverLimit,
		RemainingFraction:         1.0,
		Window:                    windowForKind(kind),
		Source:                    SourceUnknown,
		Confidence:                0.0,
		LimitScopeID:              scopeID,
		CapacityObserved:          UnobservedCapacity,
		CommittedSinceObservation: 0,
		WindowID:                  "",
		Version:                   1,
	}
}

// windowForKind returns a reasonable default BucketWindow for kind.
func windowForKind(kind QuotaDomainKind) BucketWindow {
	switch kind {
	case QuotaDomainAPIProject:
		return BucketWindowDay
	case QuotaDomainSubscriptionWindow:
		return BucketWindowMonth
	case QuotaDomainSharedPool:
		return BucketWindowWeek
	default:
		return BucketWindowRolling
	}
}
