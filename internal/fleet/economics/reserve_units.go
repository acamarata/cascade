// Purpose: the one published estimate -> dimension table. The ledger
//
//	admits against topology's closed per-kind dimension names
//	(topology/dimensions.go); this file declares no dimension of its
//	own, only how many units of each dimension an Estimate consumes.
//
// Inputs: an Estimate. Outputs: the units one reservation holds on one
//
//	(kind, dimension).
//
// Constraints: one row per closed (kind, dimension) of topology, data
//
//	only. Subscription and pool dimensions count requests: no other unit
//	is defined for them yet; a later unit change edits a row here.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import "github.com/acamarata/cascade/internal/fleet/topology"

// UnitFn returns how many units of one dimension an Estimate holds.
type UnitFn func(e Estimate) int64

// unitsRequests counts provider requests.
func unitsRequests(e Estimate) int64 { return e.Requests }

// unitsTokens counts input plus output tokens.
func unitsTokens(e Estimate) int64 { return e.TokensIn + e.TokensOut }

// ReservationUnits is the published estimate -> dimension map, keyed by
// topology's domain kind and closed dimension name. A (kind, dimension)
// without a row is refused at admission, never admitted.
var ReservationUnits = map[topology.QuotaDomainKind]map[string]UnitFn{
	topology.QuotaDomainAPIProject: {
		topology.DimensionRPM: unitsRequests,
		topology.DimensionRPD: unitsRequests,
		topology.DimensionTPM: unitsTokens,
	},
	topology.QuotaDomainSubscriptionWindow: {
		topology.DimensionSession5h:           unitsRequests,
		topology.DimensionWeeklyShared:        unitsRequests,
		topology.DimensionWeeklyModelFraction: unitsRequests,
		topology.DimensionMonthly:             unitsRequests,
	},
	topology.QuotaDomainSharedPool: {
		topology.DimensionWindow5h: unitsRequests,
		topology.DimensionWeekly:   unitsRequests,
		topology.DimensionMonthly:  unitsRequests,
	},
}
