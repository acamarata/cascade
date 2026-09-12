// Purpose: R-21.26's closed, per-QuotaDomainKind dimension-name sets and
//
//	their validation, plus the two R-21.116 batch-gauge names
//	(concurrent_requests, enqueued_tokens) window_map.go's WindowFor
//	refuses as pressure inputs.
//
// Inputs: a QuotaDomainKind and a dimension name (or a full
//
//	map[string]Bucket). Outputs: bool / ErrTopologyInvariant.
//
// Constraints: the three sets are CLOSED -- a name outside its kind's set
//
//	is ErrTopologyInvariant, never silently accepted.
//
// SPORT: fleet/topology/dimensions/ADD (P1-E40-W9-S77-T3).

package topology

// The nine closed dimension names, grouped by the QuotaDomainKind that
// accepts them (R-21.26).
const (
	DimensionRPM = "rpm"
	DimensionTPM = "tpm"
	DimensionRPD = "rpd"

	DimensionSession5h           = "session_5h"
	DimensionWeeklyShared        = "weekly_shared"
	DimensionWeeklyModelFraction = "weekly_model_fraction"
	DimensionMonthly             = "monthly"

	DimensionWindow5h = "window_5h"
	DimensionWeekly   = "weekly"
)

// GaugeConcurrentRequests and GaugeEnqueuedTokens are the R-21.116 batch
// gauge names: eligibility inputs only ("reservable iff gauge + estimate
// <= limit"), never a dimension a Bucket or WindowFor accepts.
const (
	GaugeConcurrentRequests = "concurrent_requests"
	GaugeEnqueuedTokens     = "enqueued_tokens"
)

// dimensionSets is the CLOSED per-kind allow-list, keyed by QuotaDomainKind.
var dimensionSets = map[QuotaDomainKind]map[string]bool{
	QuotaDomainAPIProject: {
		DimensionRPM: true, DimensionTPM: true, DimensionRPD: true,
	},
	QuotaDomainSubscriptionWindow: {
		DimensionSession5h: true, DimensionWeeklyShared: true,
		DimensionWeeklyModelFraction: true, DimensionMonthly: true,
	},
	QuotaDomainSharedPool: {
		DimensionWindow5h: true, DimensionWeekly: true, DimensionMonthly: true,
	},
}

// ValidDimensionName reports whether name is a permitted dimension for
// kind, per the closed per-kind set. An unrecognised kind is never valid
// for any name.
func ValidDimensionName(kind QuotaDomainKind, name string) bool {
	set, ok := dimensionSets[kind]
	if !ok {
		return false
	}
	return set[name]
}

// ValidateDimensions checks every key of dims against kind's closed set,
// returning ErrTopologyInvariant on the first name outside it.
func ValidateDimensions(kind QuotaDomainKind, dims map[string]Bucket) error {
	for name := range dims {
		if !ValidDimensionName(kind, name) {
			return newInvariantErr("quota_domain_dimension", name, "dimension name is not permitted for this domain kind")
		}
	}
	return nil
}

// BatchLimits is QuotaDomain.Batch's shape: the two R-21.116 gauges that
// gate eligibility only and are never fed into a pressure computation.
type BatchLimits struct {
	Supported          bool  `json:"supported"`
	ConcurrentRequests int   `json:"concurrent_requests"`
	EnqueuedTokenLimit int64 `json:"enqueued_token_limit"`
}

// GaugeReservable reports whether adding estimate to current would still
// satisfy limit, per R-21.116's "reservable iff gauge + estimate <= limit"
// eligibility rule. limit <= 0 means the gauge is not configured and never
// gates (always reservable), matching BatchLimits.Supported=false's
// meaning.
func GaugeReservable(current, estimate, limit int64) bool {
	if limit <= 0 {
		return true
	}
	return current+estimate <= limit
}
