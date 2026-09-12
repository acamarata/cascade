// Purpose: R-21.29's batch sibling bucket and interaction_class: batch
//
//	lane. A PAID api domain whose batch.supported is true exposes the two
//	R-21.116 gauges beside its rpm/tpm/rpd dimensions; the reconcile
//	derives a batch lane over it. A batch lane is never selected for an
//	interactive request and vice versa.
//
// Inputs: a QuotaDomain (and, for the lane, its RuntimeProfile). Outputs:
//
//	a Bucket / Lane, or (zero value, false) when the domain does not
//	qualify.
//
// Constraints: only a PAID api_project domain with batch.supported
//
//	qualifies -- a free or discover-tier domain, or one with
//	batch.supported false, never gets a batch bucket or lane, closing off
//	the shortcut of using a batch lane to evade the paid-tier limits an
//	interactive lane on the same domain already observes.
//
// SPORT: fleet/topology/batch/ADD (P1-E40-W9-S78-T1).

package topology

// batchBucketName names the sibling bucket DeriveBatchBucket returns.
// Distinct from the two gauge constants (dimensions.go) -- the BUCKET
// carries the same {concurrent_requests, enqueued_tokens} shape as a
// pressure-scored value, whereas the raw gauge names remain
// eligibility-only inputs window_map.go's WindowFor refuses.
const batchBucketName = "batch_sibling"

// qualifiesForBatch reports whether d is a paid api_project domain with
// batch.supported -- the sole R-21.29 gate for both the sibling bucket and
// the derived batch lane.
func qualifiesForBatch(d QuotaDomain) bool {
	return d.Kind == QuotaDomainAPIProject && d.BillingTier == BillingTierPaid && d.Batch.Supported
}

// DeriveBatchBucket returns d's sibling bucket over its two R-21.116
// gauges, (Bucket{}, false) when d does not qualify. The returned Bucket
// carries BucketWindowRolling (the gauges are concurrency/backlog
// counters, not a fixed-window budget) and SourceCLIObservation, since the
// gauge values themselves come from this process's own in-flight
// accounting, never a provider-reported window.
func DeriveBatchBucket(d QuotaDomain) (Bucket, bool) {
	if !qualifiesForBatch(d) {
		return Bucket{}, false
	}
	return Bucket{
		Name:              batchBucketName,
		Limit:             d.Batch.EnqueuedTokenLimit,
		RemainingFraction: 1,
		Window:            BucketWindowRolling,
		Source:            SourceCLIObservation,
		Confidence:        1,
	}, true
}

// DeriveBatchLane returns the interaction_class: batch lane reconcile
// derives over d for runtime profile p, (Lane{}, false) when d does not
// qualify. The returned Lane shares d's identity inputs with its
// interactive sibling except InteractionClass, so DeriveLaneID (R-21.124)
// assigns it a DISTINCT LaneID -- an interactive request's Candidates call
// (candidates.go) never matches it and a batch request's call never
// matches the interactive one, because eligibility is evaluated per lane,
// keyed by this distinct identity.
func DeriveBatchLane(d QuotaDomain, p RuntimeProfile) (Lane, bool) {
	if !qualifiesForBatch(d) {
		return Lane{}, false
	}
	lane := Lane{
		RuntimeProfileRef: p.ID,
		QuotaDomainRef:    d.ID,
		InteractionClass:  InteractionBatch,
		LaneClass:         LaneClassAPIBatch,
	}
	lane.ID = LaneIDFor(lane.RuntimeProfileRef, lane.QuotaDomainRef, "", lane.ModelID, lane.Effort, lane.InteractionClass)
	return lane, true
}
