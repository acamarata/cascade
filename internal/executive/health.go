// Package executive holds the credential-free executive views over fleet
// state.
//
// Purpose: project FleetRuntime.Health's map into the closed five-key
//
//	R-21.39 health summary. It copies key, state and percentage and
//	nothing else; it adds no grouping or scoring policy of its own.
//
// Inputs: a map[economics.HealthKey]economics.KeyHealth, or a HealthFunc
//
//	that returns one.
//
// Outputs: HealthSummary, five keys in a fixed order.
// Constraints: fail closed -- an unknown key or state, or a source error,
//
//	returns a zero summary and an error, never an all-healthy one. A
//	missing key is unavailable with no percentage.
package executive

import (
	"context"
	"errors"
	"fmt"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
)

// HealthFunc is the shape of FleetRuntime.Health: the five-key health map
// over the live topology rows, or (nil, err) on a read error.
type HealthFunc func(ctx context.Context) (map[economics.HealthKey]economics.KeyHealth, error)

// HealthSummaryKey is one key's projected reading. Percentage is omitted
// when the source reports it unknown.
type HealthSummaryKey struct {
	Key        economics.HealthKey   `json:"key"`
	State      economics.HealthState `json:"state"`
	Percentage *float64              `json:"percentage,omitempty"`
}

// HealthSummary is the R-21.39 five-key summary, in the order executive,
// build, bulk, adversary, long_context.
type HealthSummary struct {
	Keys [5]HealthSummaryKey `json:"keys"`
}

// ErrHealthSource reports that the health source returned a key or state
// outside the closed vocabularies.
var ErrHealthSource = errors.New("executive: health source returned an unknown key or state")

// summaryOrder is the fixed R-21.39 key order of HealthSummary.Keys.
var summaryOrder = [5]economics.HealthKey{
	economics.HealthKeyExecutive,
	economics.HealthKeyBuild,
	economics.HealthKeyBulk,
	economics.HealthKeyAdversary,
	economics.HealthKeyLongContext,
}

// validState reports whether s is one of the six closed HealthState members.
func validState(s economics.HealthState) bool {
	switch s {
	case economics.HealthStateUnavailable, economics.HealthStateProtected, economics.HealthStateScarce,
		economics.HealthStateAbundant, economics.HealthStateAvailable, economics.HealthStateHealthy:
		return true
	}
	return false
}

// BuildHealthSummary projects src into the five-key summary. An unknown
// key or state returns a zero summary and ErrHealthSource naming the key;
// a missing key (or a nil map) is reported unavailable with no percentage.
// Percentages are copied so later changes to src cannot alter the result.
func BuildHealthSummary(src map[economics.HealthKey]economics.KeyHealth) (HealthSummary, error) {
	for key, kh := range src {
		if !key.Valid() || !validState(kh.State) {
			return HealthSummary{}, fmt.Errorf("%w: key %q", ErrHealthSource, string(key))
		}
	}
	var out HealthSummary
	for i, key := range summaryOrder {
		entry := HealthSummaryKey{Key: key, State: economics.HealthStateUnavailable}
		if kh, ok := src[key]; ok {
			entry.State = kh.State
			if kh.Percentage != nil {
				p := *kh.Percentage
				entry.Percentage = &p
			}
		}
		out.Keys[i] = entry
	}
	return out, nil
}

// SummarizeFleetHealth calls health exactly once and projects its result.
// A nil health is KindInvalidInput; a source error is returned wrapped as
// KindUnavailable with a zero summary.
func SummarizeFleetHealth(ctx context.Context, health HealthFunc) (HealthSummary, error) {
	if health == nil {
		return HealthSummary{}, cascade.New(cascade.KindInvalidInput, "executive: nil health source")
	}
	src, err := health(ctx)
	if err != nil {
		return HealthSummary{}, cascade.Wrapf(cascade.KindUnavailable, err, "executive: fleet health")
	}
	return BuildHealthSummary(src)
}
