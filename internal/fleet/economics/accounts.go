// Package economics implements the R-21.24 phase-economics accounting
// layer: subscription-window Bucket accounting (window.go), account-role
// policy and reserve/executive-overflow derivation (this file), and the
// R-21.52 fleet health-summary projection (health.go).
//
// Purpose (this file): R-21.34's account-role policy -- role defaults and their
//
//	fail-closed resolution, the provider-published-cap clamp on the two
//	executive-model-fraction eligibility caps, the ReserveState
//	boundaries the R-21.31 barrier band uses, the executive-overflow
//	effective-lane-class derivation (R-21.45), the hard-reserve role
//	ladder (R-21.92), the single-executive-account default (R-21.110b)
//	and the project-share inputs (R-21.131).
//
// Inputs: an AccountRole / raw role string, an AccountPolicy, a
//
//	published-cap Bucket, a remaining fraction and reserve, a Lane and
//	Account, a ReserveState, and a []Account.
//
// Outputs: AccountPolicy / ReserveState / effective lane class and base
//
//	price / the resolved executive AccountID / a reserve-adjusted
//	remaining fraction, or a divergence flag.
//
// Constraints: every role resolution FAILS CLOSED -- an unset or
//
//	unrecognised role never resolves to executive, the most permissive
//	value. The [0.0,0.60] preserve_weekly_reserve bound and the
//	per-domain-kind barrier bucket are NOT declared here (topology.
//	ValidateReserve / topology.BarrierBucket, R-21.117/R-21.134(c)); this
//	file calls them, never re-declares them. The project-share division
//	and ErrProjectShareExceeded refusal are NOT declared here (AO/S-79.T4,
//	R-21.131); this file ships only the inputs that division consumes.
//
// SPORT: fleet/economics/accounts/ADD (P1-E41-W9-S80-T1).
package economics

import (
	"math"
	"sync"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// AccountRole is topology.AccountRole reused, not redeclared: Account.Role
// (R-21.24) already IS this closed executive|workforce|specialist
// vocabulary, and this package never carries a second, competing copy of
// it (R-16.79 -- the tree already owns this enum in topology/enums.go).
type AccountRole = topology.AccountRole

// AccountPolicy is R-21.34's per-role economics policy: how much of a
// subscription window an account's role must keep in reserve
// (PreserveWeeklyReserve), plus the two model-fraction eligibility caps
// that gate an executive-role account's executive-class lanes only --
// they never enter the R-21.31 reserve barrier (R-21.117).
type AccountPolicy struct {
	PreserveWeeklyReserve      float64
	ExecutiveModelFractionSoft float64
	ExecutiveModelFractionHard float64
}

// The R-21.34 default constants. executiveModelFractionSoft/Hard are the
// same defaults for every role (they only ever gate an executive-role
// account's eligibility, per RoleDefaults' own doc comment); only
// PreserveWeeklyReserve varies by role.
const (
	reservePreserveExecutive  = 0.30
	reservePreserveWorkforce  = 0.15
	reservePreserveSpecialist = 0.20
	defaultFractionSoft       = 0.35
	defaultFractionHard       = 0.47
)

// RoleDefaults returns role's R-21.34 default AccountPolicy. An
// unrecognised role (should never reach here -- see ResolveAccountRole)
// falls back to the workforce row, the same fail-closed default
// ResolveAccountRole itself returns.
func RoleDefaults(role AccountRole) AccountPolicy {
	reserve := reservePreserveWorkforce
	switch role {
	case topology.AccountRoleExecutive:
		reserve = reservePreserveExecutive
	case topology.AccountRoleSpecialist:
		reserve = reservePreserveSpecialist
	case topology.AccountRoleWorkforce:
		reserve = reservePreserveWorkforce
	}
	return AccountPolicy{
		PreserveWeeklyReserve:      reserve,
		ExecutiveModelFractionSoft: defaultFractionSoft,
		ExecutiveModelFractionHard: defaultFractionHard,
	}
}

// ResolveAccountRole resolves a raw, possibly-unset or invalid role
// string into a valid AccountRole, per R-21.34/06-FORGE-SPEC §5.20's
// fail-closed rule: an unset or unrecognised role NEVER resolves to
// executive (the most permissive value) -- it resolves to workforce, the
// most constrained non-executive default, and reports diverged=true so a
// doctor pass can surface the discrepancy rather than silently widening
// permissions.
func ResolveAccountRole(raw string) (role AccountRole, diverged bool) {
	r := AccountRole(raw)
	if r.Valid() {
		return r, false
	}
	return topology.AccountRoleWorkforce, true
}

// publishedCapFraction reads publishedCap as a [0,1] fraction, honouring
// R-21.34's "NEVER above a provider-published model fraction cap" only
// when the bucket's source is provider-status or cli-observation (window.
// go's own ladder precedence); a user-estimate or unknown bucket carries
// no real ceiling and never clamps (+Inf).
//
// DESIGN DECISION (undocumented in the corpus, recorded here per R-16.79).
// Bucket.Limit is a frozen int64 (R-21.26) with no float counterpart, so a
// weekly_model_fraction bucket's published cap is carried as an integer
// PERCENTAGE of capacity in [0,100] rather than a raw [0,1] fraction --
// e.g. Limit=40 clamps to the fraction 0.40.
func publishedCapFraction(publishedCap topology.Bucket) float64 {
	if !isAuthoritative(publishedCap.Source) {
		return math.Inf(1)
	}
	return float64(publishedCap.Limit) / 100.0
}

// ClampFractionCaps returns policy with its two executive-model-fraction
// caps clamped so neither exceeds publishedCap's ceiling (R-21.34):
// effective_hard = min(configured_hard, published cap); effective_soft =
// min(configured_soft, effective_hard). PreserveWeeklyReserve passes
// through unchanged -- it is never part of this clamp.
func ClampFractionCaps(policy AccountPolicy, publishedCap topology.Bucket) AccountPolicy {
	capFraction := publishedCapFraction(publishedCap)
	hard := policy.ExecutiveModelFractionHard
	if capFraction < hard {
		hard = capFraction
	}
	soft := policy.ExecutiveModelFractionSoft
	if hard < soft {
		soft = hard
	}
	return AccountPolicy{
		PreserveWeeklyReserve:      policy.PreserveWeeklyReserve,
		ExecutiveModelFractionSoft: soft,
		ExecutiveModelFractionHard: hard,
	}
}

// ReserveState is the R-21.31 barrier-band position a domain's
// remaining_fraction and reserve resolve to. Computed purely from those
// two numbers -- ExecutiveModelFractionSoft/Hard play no part in it.
type ReserveState string

// The three closed ReserveState members.
const (
	ReserveStateAbundant ReserveState = "abundant"
	ReserveStateSoft     ReserveState = "soft"
	ReserveStateHard     ReserveState = "hard"
)

// Valid reports whether s is one of the three declared members.
func (s ReserveState) Valid() bool {
	switch s {
	case ReserveStateAbundant, ReserveStateSoft, ReserveStateHard:
		return true
	}
	return false
}

// reserveBarrierBand is the R-21.31 soft-band width (reserve, reserve+0.10].
const reserveBarrierBand = 0.10

// ComputeReserveState returns abundant (remainingFraction > reserve+0.10),
// soft (reserve < remainingFraction <= reserve+0.10, the linear barrier
// band) or hard (remainingFraction <= reserve). Named ComputeReserveState
// rather than the contract's literal "ReserveState(...)" because that
// identifier already names this file's own ReserveState TYPE two lines
// above -- a same-named function would not compile (R-16.79, mirrors
// P1-E40-W9-S77-T3's identical TakeQuotaSnapshot rename).
func ComputeReserveState(remainingFraction, reserve float64) ReserveState {
	switch {
	case remainingFraction > reserve+reserveBarrierBand:
		return ReserveStateAbundant
	case remainingFraction <= reserve:
		return ReserveStateHard
	default:
		return ReserveStateSoft
	}
}

// executiveOverflowClass and executiveOverflowBasePrice are R-21.34/
// R-21.45's derived effective lane class and its base shadow price -- the
// ONE place this constant exists in the tree. executive-overflow is
// deliberately absent from topology's R-21.30 LaneClass enum
// (lane_class.go): it is a scheduling-time derivation, never a stored
// value.
const (
	executiveOverflowClass     = "executive-overflow"
	executiveOverflowBasePrice = 6.5
)

// EffectiveLaneClass returns lane's effective class and base shadow price:
// executive-overflow at base 6.5 when lane's stored class is executive or
// executive-xhigh AND account's role is workforce (R-21.34's "a
// workforce account's executive-class lane is executive-overflow"); the
// stored lane_class/base_shadow_price unchanged in every other case.
func EffectiveLaneClass(lane topology.Lane, account topology.Account) (string, float64) {
	isExecutiveClass := lane.LaneClass == topology.LaneClassExecutive || lane.LaneClass == topology.LaneClassExecutiveXHigh
	if isExecutiveClass && account.Role == topology.AccountRoleWorkforce {
		return executiveOverflowClass, executiveOverflowBasePrice
	}
	return string(lane.LaneClass), lane.BaseShadowPrice
}

// ExecutiveOverflowEligible reports whether an executive-overflow lane is
// eligible at state, per R-21.34/R-21.45: true only at reserve state hard.
func ExecutiveOverflowEligible(state ReserveState) bool {
	return state == ReserveStateHard
}

// ExecutiveRoleLadder returns the R-21.92 fixed hard-reserve resolution
// order for the executive role: executive-overflow first, a critic-class
// lane only when no executive-overflow lane is eligible. Every other
// reserve state has no ladder to resolve (the executive role simply uses
// its normal lanes), so this returns nil.
func ExecutiveRoleLadder(state ReserveState) []string {
	if state != ReserveStateHard {
		return nil
	}
	return []string{executiveOverflowClass, string(topology.LaneClassCritic)}
}

// ResolveExecutiveAccount returns the AccountID that should be treated as
// executive for eligibility/health purposes (R-21.110b): the first
// account whose stored Role is already executive, or -- when none
// carries that role -- accounts[0] (the first configured account, in the
// order the caller passes, which must be file order). The stored role is
// never rewritten; this is a resolution only. An empty slice reports ok
// false.
func ResolveExecutiveAccount(accounts []topology.Account) (id topology.AccountID, ok bool) {
	if len(accounts) == 0 {
		return "", false
	}
	for _, a := range accounts {
		if a.Role == topology.AccountRoleExecutive {
			return a.ID, true
		}
	}
	return accounts[0].ID, true
}

// ReserveAdjustedRemaining returns barrier's remaining fraction minus
// reserve, floored at 0 -- the numerator AO/S-79.T4's project-share
// division consumes (R-21.131). This file ships no division and no
// ErrProjectShareExceeded refusal; those live once, there.
func ReserveAdjustedRemaining(barrier topology.Bucket, reserve float64) float64 {
	adjusted := barrier.RemainingFraction - reserve
	if adjusted < 0 {
		return 0
	}
	return adjusted
}

// ActiveProjectCount is the R-21.131 "active project count" source: a
// small, concurrency-safe counter recomputed on project start and on
// project stop. AO/S-79.T4's project-share division reads Count(); this
// type ships the source only, never the division itself. The zero value
// is usable (starts at 0) and Count never goes negative.
type ActiveProjectCount struct {
	mu    sync.Mutex
	count int
}

// ProjectStarted increments the count. Called once per project start.
func (c *ActiveProjectCount) ProjectStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
}

// ProjectStopped decrements the count, floored at 0 so a mismatched or
// duplicate stop call can never drive it negative.
func (c *ActiveProjectCount) ProjectStopped() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count > 0 {
		c.count--
	}
}

// Count returns the current active-project count.
func (c *ActiveProjectCount) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}
