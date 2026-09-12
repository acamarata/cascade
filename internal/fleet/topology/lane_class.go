// Purpose: the R-21.30 seventeen-row closed LaneClass enum and
//
//	BaseShadowPrice, the DATA table mapping each class to its base
//	shadow price. executive-overflow is deliberately NOT a member: R-21.45
//	makes it a DERIVED effective class computed elsewhere (AO/S-80.T1),
//	never a value of this enum.
//
// Inputs: a LaneClass value. Outputs: its base price, or ErrTopologyInvariant.
// Constraints: no vendor or model name (R-21.23); every row is pinned in
//
//	testdata/lane_class_prices.golden.json so an edit is a visible diff.
//
// SPORT: fleet/topology/lane_class/ADD (P1-E40-W9-S77-T1).

package topology

// LaneClass is the R-21.30 closed, seventeen-member lane-class vocabulary.
// The zero value is invalid.
type LaneClass string

// The seventeen closed LaneClass members.
const (
	LaneClassExecutive       LaneClass = "executive"
	LaneClassExecutiveXHigh  LaneClass = "executive-xhigh"
	LaneClassCritic          LaneClass = "critic"
	LaneClassSpecialist      LaneClass = "specialist"
	LaneClassAdvisor         LaneClass = "advisor"
	LaneClassLead            LaneClass = "lead"
	LaneClassDeep            LaneClass = "deep"
	LaneClassHarnessSub      LaneClass = "harness-sub"
	LaneClassWorkerDedicated LaneClass = "worker-dedicated"
	LaneClassWorkerFast      LaneClass = "worker-fast"
	LaneClassPoolPremium     LaneClass = "pool-premium"
	LaneClassPoolCheap       LaneClass = "pool-cheap"
	LaneClassAPIPaid         LaneClass = "api-paid"
	LaneClassAPIFree         LaneClass = "api-free"
	LaneClassAPIBatch        LaneClass = "api-batch"
	LaneClassLocal           LaneClass = "local"
	LaneClassUnranked        LaneClass = "unranked"
)

// Valid reports whether c is one of the seventeen declared members.
func (c LaneClass) Valid() bool {
	_, ok := laneClassPrices[c]
	return ok
}

// String returns c's wire form.
func (c LaneClass) String() string { return string(c) }

// laneClassPrices is the R-21.30 base-shadow-price table, keyed by the
// closed LaneClass vocabulary. This IS the ratified data, kept in
// declaration order matching R-21.30's own listing; a JSON golden
// (testdata/lane_class_prices.golden.json, pinned by
// TestBaseShadowPriceGolden) pins every row so an edit is a visible diff.
var laneClassPrices = map[LaneClass]float64{
	LaneClassExecutive:       12.0,
	LaneClassExecutiveXHigh:  16.0,
	LaneClassCritic:          8.0,
	LaneClassSpecialist:      10.0,
	LaneClassAdvisor:         7.0,
	LaneClassLead:            2.5,
	LaneClassDeep:            4.5,
	LaneClassHarnessSub:      2.0,
	LaneClassWorkerDedicated: 1.1,
	LaneClassWorkerFast:      0.55,
	LaneClassPoolPremium:     2.5,
	LaneClassPoolCheap:       0.35,
	LaneClassAPIPaid:         0.25,
	LaneClassAPIFree:         0.10,
	LaneClassAPIBatch:        0.15,
	LaneClassLocal:           0.05,
	LaneClassUnranked:        1.0,
}

// orderedLaneClasses is laneClassPrices's key set in the same declaration
// order as the const block above, used by golden-file generation/assertion
// so the JSON row order is deterministic across runs (never map
// iteration).
var orderedLaneClasses = []LaneClass{
	LaneClassExecutive, LaneClassExecutiveXHigh, LaneClassCritic, LaneClassSpecialist,
	LaneClassAdvisor, LaneClassLead, LaneClassDeep, LaneClassHarnessSub,
	LaneClassWorkerDedicated, LaneClassWorkerFast, LaneClassPoolPremium, LaneClassPoolCheap,
	LaneClassAPIPaid, LaneClassAPIFree, LaneClassAPIBatch, LaneClassLocal, LaneClassUnranked,
}

// BaseShadowPrice returns class's R-21.30 base shadow price. An unknown
// class returns ErrTopologyInvariant -- personal-config overrides of these
// prices belong to AO/S-80.T1 and are not implemented here.
func BaseShadowPrice(class LaneClass) (float64, error) {
	price, ok := laneClassPrices[class]
	if !ok {
		return 0, newInvariantErr("lane_class", string(class), "unknown lane class")
	}
	return price, nil
}
