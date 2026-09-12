// Purpose: HealthKeys' HealthState resolution -- domainSetContext (the
//   per-domain reserve/pressure lookups), the R-21.52 minimum-
//   remaining_fraction percentage rule, and keyState's ordered rule chain
//   (unavailable/protected/scarce/abundant/available/healthy). Split from
//   health.go per Art.10.3's 300-line cap (health.go's own types plus
//   this logic together crossed it).
// Inputs: a topology.QuotaSnapshot/[]topology.Lane-derived domain-id set,
//   the domain-kind/account lookups HealthKeys builds once, and an
//   injected Clock.
// Outputs: a HealthState per key, and the reported percentage.
// Constraints: R-21.53 -- scarce/abundant read topology.BucketPressure/
//   DomainPressure (AN/S-78.T1) as the single owned pressure computation;
//   this file declares no second one.
// SPORT: fleet/economics/health/ADD (P1-E41-W9-S80-T1).

package economics

import "github.com/acamarata/cascade/internal/fleet/topology"

// scarcePressureFloor/abundantPressureCeiling/abundantHealthyLaneFloor are
// R-21.52's own literal thresholds ("minimum quota pressure > 5.0",
// "pressure < 2.0", ">= 3 lanes healthy").
const (
	scarcePressureFloor      = 5.0
	abundantPressureCeiling  = 2.0
	abundantHealthyLaneFloor = 3
	// noEWMA429Signal is the ewma429 input topology.BucketPressure/
	// DomainPressure take for R-21.128's unknown-source pricing.
	// HealthKeys has no per-scope 429-rate tracker wired to it (that
	// state lives with whatever owns the scope's dispatch history, not
	// this read-only projection), so it passes 0 -- the value ewma429
	// naturally converges to absent any observed 429s, never a fabricated
	// non-zero rate.
	noEWMA429Signal = 0.0
)

// minRemainingFraction returns the minimum RemainingFraction across every
// dimension of every domain in domainIDs, and whether at least one
// non-unknown-source bucket contributed (R-21.52's omitted-when-unknown
// rule).
func minRemainingFraction(snapshot topology.QuotaSnapshot, domainIDs map[topology.DomainID]bool) (float64, bool) {
	minVal := 1.0
	found := false
	knownAny := false
	for _, d := range snapshot.Domains {
		if !domainIDs[d.DomainID] {
			continue
		}
		for _, b := range d.Dimensions {
			found = true
			if b.Source != topology.SourceUnknown {
				knownAny = true
			}
			if b.RemainingFraction < minVal {
				minVal = b.RemainingFraction
			}
		}
	}
	if !found || !knownAny {
		return 0, false
	}
	return minVal, true
}

// domainSetContext bundles the per-domain lookups keyState's helpers need,
// factored out so HealthKeys itself stays under the funlen cap.
type domainSetContext struct {
	snapshot      topology.QuotaSnapshot
	byID          map[topology.DomainID]topology.DomainQuota
	domainKind    map[topology.DomainID]topology.QuotaDomainKind
	domainAccount map[topology.DomainID]topology.AccountID
	accounts      map[topology.AccountID]topology.Account
	now           Clock
}

// reserveFor returns the RoleDefaults reserve for the account owning id.
func (c domainSetContext) reserveFor(id topology.DomainID) float64 {
	account := c.accounts[c.domainAccount[id]]
	return RoleDefaults(account.Role).PreserveWeeklyReserve
}

// domainReserveHard reports whether any domain in domainIDs is at reserve
// state hard, using the R-21.117 barrier bucket and RoleDefaults(account.
// Role) as the reserve (a caller with a configured, possibly-overridden
// AccountPolicy should pass its own reserve computation in a future
// revision; this ticket's HealthKeys has no config input, so the role
// default is the honest value available here).
func (c domainSetContext) domainReserveHard(domainIDs map[topology.DomainID]bool) bool {
	for id := range domainIDs {
		kind, ok := c.domainKind[id]
		if !ok {
			continue
		}
		barrierName, err := topology.BarrierBucket(kind)
		if err != nil {
			continue
		}
		dq, ok := c.byID[id]
		if !ok {
			continue
		}
		bucket, ok := dq.Dimensions[barrierName]
		if !ok {
			continue
		}
		if ComputeReserveState(bucket.RemainingFraction, c.reserveFor(id)) == ReserveStateHard {
			return true
		}
	}
	return false
}

// minDomainPressure returns the MINIMUM topology.DomainPressure across
// domainIDs (R-21.52's own "the set's minimum quota pressure" wording),
// and whether at least one domain contributed. A domain absent from the
// snapshot or an unrecognised kind is skipped rather than guessed.
func (c domainSetContext) minDomainPressure(domainIDs map[topology.DomainID]bool) (float64, bool) {
	minVal := 0.0
	found := false
	for id := range domainIDs {
		dq, ok := c.byID[id]
		if !ok {
			continue
		}
		kind, ok := c.domainKind[id]
		if !ok {
			continue
		}
		qd := topology.QuotaDomain{ID: id, Kind: kind, Dimensions: dq.Dimensions}
		p := topology.DomainPressure(qd, c.reserveFor(id), noEWMA429Signal, c.now.Now())
		if !found || p < minVal {
			minVal = p
			found = true
		}
	}
	return minVal, found
}

// keyState resolves key's HealthState from its member lanes (already
// filtered into laneSet) and the domains those lanes reference, per
// R-21.52's ordered rules: unavailable, then (executive only) protected,
// then scarce, then (bulk only) abundant, then available, else healthy.
func keyState(ctx domainSetContext, key HealthKey, laneSet []topology.Lane, domainIDs map[topology.DomainID]bool) HealthState {
	if len(laneSet) == 0 || allQuarantinedOrAuthRequired(laneSet) {
		return HealthStateUnavailable
	}
	if key == HealthKeyExecutive && ctx.domainReserveHard(domainIDs) {
		return HealthStateProtected
	}
	minPressure, havePressure := ctx.minDomainPressure(domainIDs)
	if havePressure && minPressure > scarcePressureFloor {
		return HealthStateScarce
	}
	healthyCount := countHealthy(laneSet)
	if key == HealthKeyBulk && healthyCount >= abundantHealthyLaneFloor && havePressure && minPressure < abundantPressureCeiling {
		return HealthStateAbundant
	}
	if healthyCount > 0 {
		return HealthStateAvailable
	}
	return HealthStateHealthy
}

func allQuarantinedOrAuthRequired(lanes []topology.Lane) bool {
	for _, l := range lanes {
		if l.Health != topology.LaneHealthQuarantined && l.Health != topology.LaneHealthAuthRequired {
			return false
		}
	}
	return true
}

func countHealthy(lanes []topology.Lane) int {
	n := 0
	for _, l := range lanes {
		if l.Health == topology.LaneHealthAvailable {
			n++
		}
	}
	return n
}
