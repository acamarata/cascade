// Purpose: subscription-window Bucket accounting (R-21.26/R-21.34) --
//   WindowAccountant.Buckets emits the four subscription_window
//   dimensions for a domain by reading the AN/S-77.T3 quota-domain
//   buckets (authoritative whenever provider-status or cli-observation)
//   and, when no higher-precedence source exists for a dimension,
//   synthesising a user-estimate bucket from the J/S-20.T4 usage
//   accounting domain. A dimension with no input at all is still
//   emitted, at source unknown, per R-21.26's "unknown is not infinite"
//   rule.
//
// Inputs: a topology.DomainID naming a subscription_window QuotaDomain,
//
//	a *topology.Store (accounts/domains), a *topology.QuotaStore
//	(buckets), a pkg/provider.UsageReader (elapsed spend) and an
//	injected Clock.
//
// Outputs: exactly the four R-21.26 subscription_window Bucket values, or
//
//	a pkg/cascade taxonomy error.
//
// Constraints: source precedence is applied per DIMENSION, never per
//
//	domain (R-21.26). All time reads use the injected Clock -- never bare
//	time.Now (Art.7.3).
//
// SPORT: fleet/economics/window/ADD (P1-E41-W9-S80-T1).

package economics

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Clock abstracts the wall clock (Art.7.3), structurally identical to
// every other package's own local Clock (e.g.
// internal/providers/usage.Clock), so testkit.FrozenClock/RealClock
// satisfy it with zero adapter code.
type Clock interface {
	Now() time.Time
}

// subscriptionWindowDimensions is the exact, ordered R-21.26 dimension
// set Buckets emits for every subscription_window domain -- no more, no
// fewer.
var subscriptionWindowDimensions = []string{
	topology.DimensionSession5h,
	topology.DimensionWeeklyShared,
	topology.DimensionWeeklyModelFraction,
	topology.DimensionMonthly,
}

// WindowAccountant computes the four R-21.26 subscription_window Bucket
// values for a domain from the two inputs R-21.26 permits: the AN/S-77.T3
// quota-domain buckets (authoritative at provider-status/cli-observation)
// and the J/S-20.T4 usage accounting domain (synthesises a user-estimate
// bucket). The zero value is not usable; construct with
// NewWindowAccountant.
type WindowAccountant struct {
	store  *topology.Store
	quotas *topology.QuotaStore
	usage  provider.UsageReader
	clock  Clock
}

// NewWindowAccountant returns a WindowAccountant reading accounts/domains
// from store, buckets from quotas, elapsed spend from usage, and stamping
// every synthesised/unknown bucket's reset_at from clock.
func NewWindowAccountant(store *topology.Store, quotas *topology.QuotaStore, usage provider.UsageReader, clock Clock) *WindowAccountant {
	return &WindowAccountant{store: store, quotas: quotas, usage: usage, clock: clock}
}

// Buckets emits exactly the four subscription_window dimensions for
// domainID, resolved per-dimension through the source ladder
// provider-status > cli-observation > user-estimate > unknown. domainID
// must name a subscription_window QuotaDomain; any other kind is
// ErrTopologyInvariant via the domain-kind check below.
func (w *WindowAccountant) Buckets(ctx context.Context, domainID topology.DomainID) ([]topology.Bucket, error) {
	if w == nil || w.store == nil || w.quotas == nil || w.usage == nil || w.clock == nil {
		return nil, cascade.New(cascade.KindUnavailable, "economics: WindowAccountant requires a store, quota store, usage reader and clock")
	}
	domain, err := w.store.GetQuotaDomain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	if domain.Kind != topology.QuotaDomainSubscriptionWindow {
		return nil, cascade.New(cascade.KindInvalidInput, "economics: window accounting requires a subscription_window domain, got "+string(domain.Kind))
	}
	account, err := w.store.GetAccount(ctx, domain.AccountRef)
	if err != nil {
		return nil, err
	}
	existing, err := w.quotas.ListBuckets(ctx, domainID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]topology.Bucket, len(existing))
	for _, b := range existing {
		byName[b.Name] = b
	}

	now := w.clock.Now()
	out := make([]topology.Bucket, 0, len(subscriptionWindowDimensions))
	for _, dim := range subscriptionWindowDimensions {
		if b, ok := byName[dim]; ok && isAuthoritative(b.Source) {
			out = append(out, b)
			continue
		}
		if b, ok := w.estimateFromUsage(ctx, account, dim, now); ok {
			out = append(out, b)
			continue
		}
		b, err := unknownBucket(dim, now)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// isAuthoritative reports whether source is one of the two
// higher-than-user-estimate R-21.26 sources this ticket treats as
// authoritative for an existing AN/S-77.T3 bucket.
func isAuthoritative(source topology.BucketSource) bool {
	return source == topology.SourceProviderStatus || source == topology.SourceCLIObservation
}

// usageActivityDivisor and estimateMinRemaining shape
// estimateFromUsage's activity-based decay curve (see its doc comment).
const (
	usageActivityDivisor = 100.0
	estimateMinRemaining = 0.10
	estimateConfidence   = 0.4
)

// estimateFromUsage synthesises a user-estimate Bucket for dimension dim
// from the J/S-20.T4 usage accounting domain, when no higher-precedence
// source exists. It sums Requests across every provider_usage row within
// the dimension's window (WindowFor's length, ending at now) for
// account.Provider, as the "elapsed spend" R-21.26 calls for. Returns
// ok=false when the usage reader has recorded ZERO requests in the
// window -- the caller then falls back to unknownBucket, since an
// estimate synthesised from zero signal would just be a disguised guess.
//
// DESIGN DECISION (undocumented in the corpus, recorded here per R-16.79
// and in this ticket's journal). A provider_usage row carries token/
// request COUNTS, never the account's total subscription capacity, so
// this domain has no way to compute a true remaining-capacity fraction
// from usage alone. The estimate reports the only thing request-volume
// activity can honestly support: a bounded, monotonically decreasing
// function of total requests observed in the window
// (1/(1+requests/100), floored at 0.10 so a user-estimate bucket is
// never reported as if fully exhausted -- a guess must never assert
// exhaustion, only a real observation may). Confidence is fixed at 0.4,
// well below any live observation's, so the scarcity scheduler never
// over-trusts a synthesised reading.
func (w *WindowAccountant) estimateFromUsage(ctx context.Context, account topology.Account, dim string, now time.Time) (topology.Bucket, bool) {
	window, length, err := topology.WindowFor(dim)
	if err != nil {
		return topology.Bucket{}, false
	}
	since := now.Add(-length)
	summaries, err := w.usage.QueryUsage(ctx, provider.UsageFilter{ProviderName: account.Provider, Since: since, Until: now})
	if err != nil {
		return topology.Bucket{}, false
	}
	var totalRequests int64
	var lastUsed time.Time
	for _, s := range summaries {
		totalRequests += s.Requests
		if s.LastUsedAt.After(lastUsed) {
			lastUsed = s.LastUsedAt
		}
	}
	if totalRequests == 0 {
		return topology.Bucket{}, false
	}

	remaining := 1.0 / (1.0 + float64(totalRequests)/usageActivityDivisor)
	if remaining < estimateMinRemaining {
		remaining = estimateMinRemaining
	}
	observedAt := lastUsed
	if observedAt.IsZero() {
		observedAt = now
	}
	b, err := topology.NewBucket(topology.Bucket{
		Name:              dim,
		Limit:             topology.DiscoverLimit,
		RemainingFraction: remaining,
		ResetAt:           now.Add(length),
		Window:            window,
		Source:            topology.SourceUserEstimate,
		Confidence:        estimateConfidence,
		ObservedAt:        observedAt,
		CapacityObserved:  topology.UnobservedCapacity,
	})
	if err != nil {
		return topology.Bucket{}, false
	}
	return b, true
}

// unknownBucket emits dim's R-21.26 "no input at all" reading: source
// unknown, limit -1 (discover, never treated as unlimited), confidence 0,
// remaining_fraction 0 (the conservative default -- a dimension never
// observed reports no known headroom, rather than a false abundant
// reading). reset_at/window still come from the mandatory window map on
// the injected clock, so a caller never divides by a zero window length.
func unknownBucket(dim string, now time.Time) (topology.Bucket, error) {
	window, length, err := topology.WindowFor(dim)
	if err != nil {
		return topology.Bucket{}, err
	}
	return topology.NewBucket(topology.Bucket{
		Name:              dim,
		Limit:             topology.DiscoverLimit,
		RemainingFraction: 0,
		ResetAt:           now.Add(length),
		Window:            window,
		Source:            topology.SourceUnknown,
		Confidence:        0,
		ObservedAt:        now,
		CapacityObserved:  topology.UnobservedCapacity,
	})
}
