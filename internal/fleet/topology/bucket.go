// Purpose: the R-21.26 Bucket value type -- one quota dimension's observed
//
//	capacity, its window/source enums, and Reservable, the "unknown is
//	not infinite" eligibility gate. R-21.96's absolute-accounting fields
//	(LimitScopeID, CapacityObserved, CommittedSinceObservation,
//	WindowID, Version) are added to the same struct rather than a
//	second type, since every consumer (source_ladder.go, freshness.go,
//	availability.go) needs both the R-21.26 shape and the R-21.96
//	counters on one value.
//
// Inputs: none (construction only). Outputs: a validated Bucket, or
//
//	ErrTopologyInvariant.
//
// Constraints: NewBucket fails closed -- an out-of-range RemainingFraction
//
//	or Confidence, an empty Name, or an unrecognised Window/Source is
//	refused rather than clamped or defaulted. Reservable never reads a
//	bucket as unlimited: only a bucket whose Limit is exactly 0 refuses a
//	reservation (R-21.26's own wording); a Limit of DiscoverLimit (-1,
//	"capacity not yet observed") and any Source, including SourceUnknown,
//	remain reservable and keep their lane's concurrency limits and base
//	shadow price -- neither is ever treated as unlimited.
//
// SPORT: fleet/topology/bucket/ADD (P1-E40-W9-S77-T3).

package topology

import "time"

// BucketWindow is the closed R-21.26 accounting-window vocabulary a
// Bucket resets on. The zero value is invalid.
type BucketWindow string

// The five closed BucketWindow members.
const (
	BucketWindow5h      BucketWindow = "5h"
	BucketWindowDay     BucketWindow = "day"
	BucketWindowWeek    BucketWindow = "week"
	BucketWindowMonth   BucketWindow = "month"
	BucketWindowRolling BucketWindow = "rolling"
)

// Valid reports whether w is one of the five declared members.
func (w BucketWindow) Valid() bool {
	switch w {
	case BucketWindow5h, BucketWindowDay, BucketWindowWeek, BucketWindowMonth, BucketWindowRolling:
		return true
	}
	return false
}

// BucketSource is the closed R-21.26 observation-provenance vocabulary,
// also the source_ladder.go precedence and freshness.go expiry key. The
// zero value is invalid -- an unset source is never silently SourceUnknown.
type BucketSource string

// The four closed BucketSource members, in DESCENDING precedence order
// (source_ladder.go's Merge and freshness.go's Select both key off this
// same order via precedence()).
const (
	SourceProviderStatus BucketSource = "provider-status"
	SourceCLIObservation BucketSource = "cli-observation"
	SourceUserEstimate   BucketSource = "user-estimate"
	SourceUnknown        BucketSource = "unknown"
)

// Valid reports whether s is one of the four declared members.
func (s BucketSource) Valid() bool {
	_, ok := sourcePrecedence[s]
	return ok
}

// sourcePrecedence assigns each BucketSource its precedence rank, higher
// wins. Declared once here so source_ladder.go and freshness.go share
// exactly one ranking rather than two copies that could drift apart.
var sourcePrecedence = map[BucketSource]int{
	SourceProviderStatus: 3,
	SourceCLIObservation: 2,
	SourceUserEstimate:   1,
	SourceUnknown:        0,
}

// precedence returns s's rank, or -1 for an invalid source.
func (s BucketSource) precedence() int {
	rank, ok := sourcePrecedence[s]
	if !ok {
		return -1
	}
	return rank
}

// DiscoverLimit is the R-21.26 sentinel Bucket.Limit value meaning
// "capacity not yet observed" -- distinct from 0 (a real, granted zero
// capacity) and reservable exactly like a SourceUnknown bucket.
const DiscoverLimit int64 = -1

// UnobservedCapacity is the R-21.96 sentinel Bucket.CapacityObserved value
// meaning the absolute capacity has never been reconciled from a provider
// observation.
const UnobservedCapacity int64 = -1

// LimitScopeID is limit_scope.go's stable scope identifier, embedded on
// every Bucket per R-21.95.
type LimitScopeID string

// Bucket is one quota dimension's observed capacity (R-21.26) plus its
// R-21.96 absolute-accounting counters. Construct only through NewBucket;
// the zero value fails every Valid check and is never treated as a real
// bucket.
type Bucket struct {
	Name              string       `json:"name"`
	Limit             int64        `json:"limit"`
	RemainingFraction float64      `json:"remaining_fraction"`
	ResetAt           time.Time    `json:"reset_at"`
	Window            BucketWindow `json:"window"`
	Source            BucketSource `json:"source"`
	Confidence        float64      `json:"confidence"`
	ObservedAt        time.Time    `json:"observed_at"`

	// LimitScopeID is R-21.95's stable scope reference (limit_scope.go).
	LimitScopeID LimitScopeID `json:"limit_scope_id"`
	// CapacityObserved is the absolute observed capacity for the current
	// WindowID, or UnobservedCapacity (-1) before any provider
	// reconciliation. R-21.96: a provider observation reconciles this
	// field (and WindowID/Version) and touches no reservation.
	CapacityObserved int64 `json:"capacity_observed"`
	// CommittedSinceObservation is the absolute amount consumed since
	// CapacityObserved was last reconciled, per the provider's own
	// accounting -- distinct from availability.go's reservation-derived
	// commitments, which layer on top of this value.
	CommittedSinceObservation int64 `json:"committed_since_observation"`
	// WindowID identifies the specific accounting window CapacityObserved
	// was reconciled against (e.g. a provider-issued window token),
	// opaque to this package.
	WindowID string `json:"window_id"`
	// Version increments on every reconciliation, for optimistic-
	// concurrency callers.
	Version int64 `json:"version"`
}

// NewBucket validates b against every R-21.26/R-21.96 range and
// enumeration constraint and returns it unchanged on success.
// FAIL CLOSED: an empty Name, an out-of-range RemainingFraction or
// Confidence, or an unrecognised Window or Source is ErrTopologyInvariant
// -- never clamped, never silently defaulted.
func NewBucket(b Bucket) (Bucket, error) {
	if b.Name == "" {
		return Bucket{}, newInvariantErr("bucket", "", "name must not be empty")
	}
	if b.RemainingFraction < 0 || b.RemainingFraction > 1 {
		return Bucket{}, newInvariantErr("bucket", b.Name, "remaining_fraction must be in [0,1]")
	}
	if b.Confidence < 0 || b.Confidence > 1 {
		return Bucket{}, newInvariantErr("bucket", b.Name, "confidence must be in [0,1]")
	}
	if !b.Window.Valid() {
		return Bucket{}, newInvariantErr("bucket", b.Name, "window is unrecognised")
	}
	if !b.Source.Valid() {
		return Bucket{}, newInvariantErr("bucket", b.Name, "source is unrecognised")
	}
	return b, nil
}

// Reservable reports whether a new reservation of estimate units may be
// attempted against b, per R-21.26's "unknown is not infinite" rule: only
// a bucket whose Limit is exactly 0 (a real, granted zero capacity)
// refuses. DiscoverLimit (-1, capacity not yet observed) and any Source,
// including SourceUnknown, remain reservable and keep their lane's
// concurrency limits and base shadow price -- this function never reads
// either as unlimited. A known positive Limit additionally refuses an
// estimate that could never fit even at full capacity, so a caller cannot
// use Reservable to bypass a limit smaller than its own request.
func Reservable(b Bucket, estimate int64) bool {
	if b.Limit == 0 {
		return false
	}
	if b.Limit > 0 && estimate > b.Limit {
		return false
	}
	return true
}
