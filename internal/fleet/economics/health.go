// Purpose: R-21.52's fleet health-summary derivation -- HealthKeys maps
//   the five closed health keys to their named lane sets and resolves
//   each to the six-state enum, the minimum-remaining_fraction
//   percentage rule, and the redaction guarantee AP/S-82.T2's
//   fleet.health.summary consumes.
//
// Inputs: a topology.QuotaSnapshot, the []topology.Lane rows it covers,
//
//	the []topology.QuotaDomain rows those lanes reference (for
//	AccountRef/Kind -- DomainQuota itself carries neither), the owning
//	[]topology.Account rows, and an injected Clock (Art.7.3) for the
//	R-21.31 pressure formula's time-to-reset term.
//
// Outputs: map[HealthKey]KeyHealth, total over the five closed keys.
// Constraints: never emits a credential, secret_ref or key id. R-21.53:
//
//	scarce/abundant read topology.BucketPressure/DomainPressure
//	(AN/S-78.T1) as the single owned pressure computation -- this file
//	declares no second one.
//
// SPORT: fleet/economics/health/ADD (P1-E41-W9-S80-T1).

package economics

import "github.com/acamarata/cascade/internal/fleet/topology"

// HealthKey is the R-21.52 closed key vocabulary: which lane family a
// fleet-health reading covers.
type HealthKey string

// The five closed HealthKey members.
const (
	HealthKeyExecutive   HealthKey = "executive"
	HealthKeyBuild       HealthKey = "build"
	HealthKeyBulk        HealthKey = "bulk"
	HealthKeyAdversary   HealthKey = "adversary"
	HealthKeyLongContext HealthKey = "long_context"
)

// Valid reports whether k is one of the five declared members.
func (k HealthKey) Valid() bool {
	switch k {
	case HealthKeyExecutive, HealthKeyBuild, HealthKeyBulk, HealthKeyAdversary, HealthKeyLongContext:
		return true
	}
	return false
}

// allHealthKeys is the five keys in R-21.52's own declaration order, used
// so HealthKeys' output map is always total over the closed set (never a
// partial map, even when a key's lane set is empty).
var allHealthKeys = []HealthKey{HealthKeyExecutive, HealthKeyBuild, HealthKeyBulk, HealthKeyAdversary, HealthKeyLongContext}

// HealthState is the R-21.52 closed six-state enum, in the ordered
// precedence the ruling specifies.
type HealthState string

// The six closed HealthState members, in R-21.52's precedence order.
const (
	HealthStateUnavailable HealthState = "unavailable"
	HealthStateProtected   HealthState = "protected"
	HealthStateScarce      HealthState = "scarce"
	HealthStateAbundant    HealthState = "abundant"
	HealthStateAvailable   HealthState = "available"
	HealthStateHealthy     HealthState = "healthy"
)

// KeyHealth is one HealthKey's resolved reading. Percentage is nil when
// every quota source in the key's domain set is unknown (R-21.52's
// omitted-when-unknown rule) -- never a fabricated 0 or 100.
type KeyHealth struct {
	State      HealthState
	Percentage *float64
}

// longContextThreshold is R-21.52's context_tokens floor for the
// long_context key.
const longContextThreshold = 500_000

// laneInSet reports whether lane (owned by account) belongs to key's
// R-21.52 lane set.
func laneInSet(key HealthKey, lane topology.Lane, account topology.Account) bool {
	switch key {
	case HealthKeyExecutive:
		return isExecutiveLaneClass(lane.LaneClass) && account.Role == topology.AccountRoleExecutive
	case HealthKeyBuild:
		return lane.LaneClass == topology.LaneClassLead || lane.LaneClass == topology.LaneClassDeep || lane.LaneClass == topology.LaneClassWorkerDedicated
	case HealthKeyBulk:
		return isBulkLaneClass(lane.LaneClass)
	case HealthKeyAdversary:
		return isAdversaryLaneClass(lane.LaneClass)
	case HealthKeyLongContext:
		return lane.OfferingSnapshot.ContextTokens >= longContextThreshold
	default:
		return false
	}
}

func isExecutiveLaneClass(c topology.LaneClass) bool {
	return c == topology.LaneClassExecutive || c == topology.LaneClassExecutiveXHigh
}

// isBulkLaneClass reports whether c is one of R-21.52's bulk-key member
// classes. Written as an OR-chain rather than a switch over
// topology.LaneClass so this file's exhaustive lane-class coverage over
// the R-21.30 seventeen-member enum stays limited to laneInSet's own
// per-key membership test, not a second full-enum switch per key.
func isBulkLaneClass(c topology.LaneClass) bool {
	return c == topology.LaneClassAPIPaid || c == topology.LaneClassAPIFree || c == topology.LaneClassAPIBatch ||
		c == topology.LaneClassPoolCheap || c == topology.LaneClassWorkerFast
}

// isAdversaryLaneClass reports whether c is one of R-21.52's adversary-key
// member classes.
func isAdversaryLaneClass(c topology.LaneClass) bool {
	return c == topology.LaneClassSpecialist || c == topology.LaneClassCritic || c == topology.LaneClassAdvisor
}

// HealthKeys resolves R-21.52's fleet-health projection: the five closed
// keys, each to its named lane set's HealthState and percentage.
//
// R-16.79 SIGNATURE NOTE (recorded in this ticket's journal): the
// contract's literal signature is "HealthKeys(snapshot) map[key]
// KeyHealth", but R-21.52's own key definitions are LANE-set memberships
// (executive/executive-xhigh lanes on executive-role accounts, lead/deep/
// worker-dedicated lanes, ...) that topology.QuotaSnapshot's DomainQuota
// rows cannot express -- DomainQuota carries no LaneClass, no Health, no
// AccountRef. lanes/domains/accounts/now are the minimum extra input the
// ratified definition (plus R-21.31's pressure formula, Art.7.3 clock
// injection) requires; snapshot alone cannot answer it.
//
// The returned map is total over the five keys and never contains a
// credential, secret_ref or key id: every field this function reads is
// LaneClass, LaneHealth, ContextTokens, AccountRole, or Bucket.
// RemainingFraction/Source/Name.
func HealthKeys(snapshot topology.QuotaSnapshot, lanes []topology.Lane, domains []topology.QuotaDomain, accounts map[topology.AccountID]topology.Account, now Clock) map[HealthKey]KeyHealth {
	domainKind := make(map[topology.DomainID]topology.QuotaDomainKind, len(domains))
	domainAccount := make(map[topology.DomainID]topology.AccountID, len(domains))
	for _, d := range domains {
		domainKind[d.ID] = d.Kind
		domainAccount[d.ID] = d.AccountRef
	}
	byID := make(map[topology.DomainID]topology.DomainQuota, len(snapshot.Domains))
	for _, d := range snapshot.Domains {
		byID[d.DomainID] = d
	}
	ctx := domainSetContext{snapshot: snapshot, byID: byID, domainKind: domainKind, domainAccount: domainAccount, accounts: accounts, now: now}

	out := make(map[HealthKey]KeyHealth, len(allHealthKeys))
	for _, key := range allHealthKeys {
		var laneSet []topology.Lane
		domainIDs := map[topology.DomainID]bool{}
		for _, lane := range lanes {
			account := accounts[domainAccount[lane.QuotaDomainRef]]
			if laneInSet(key, lane, account) {
				laneSet = append(laneSet, lane)
				domainIDs[lane.QuotaDomainRef] = true
			}
		}
		minRemaining, haveMin := minRemainingFraction(snapshot, domainIDs)
		var percentage *float64
		if haveMin {
			v := minRemaining
			percentage = &v
		}
		out[key] = KeyHealth{State: keyState(ctx, key, laneSet, domainIDs), Percentage: percentage}
	}
	return out
}
