package jobs

// Purpose: enforce dispatch and completion family requirements through an
// injected policy, without owning consequence rules or family resolution.
// Inputs: the review policy and already resolved provider families.
// Outputs: an explicit degradation flag or an elevation-required sentinel.
// Constraints: unresolved or empty families never prove distinctness.
// SPORT: jobs/reviewer-assignment/ADD (P1-PBD-01b).

import "github.com/acamarata/cascade/pkg/cascade"

// ReviewPolicySource supplies the consequence policy owned by the review layer.
// Its structural interface lets that layer provide its existing rules without
// introducing an upward dependency or a second consequence table in jobs.
type ReviewPolicySource interface {
	RequiresDistinctFamily() bool
	HoldsWithoutDistinctFamily() bool
}

// FamilyRef carries a provider family resolved by the caller. Family is the
// provider family, never an account name. Resolved must be true and Family must
// be nonempty before the reference can prove distinctness.
type FamilyRef struct {
	Family   string
	Resolved bool
}

// ErrNoDistinctFamily holds a review that requires a distinct reviewer family.
var ErrNoDistinctFamily = cascade.New(cascade.KindElevationRequired, "jobs: no distinct reviewer family")

// ErrHumanApprovalRequired holds completion until human approval is supplied.
var ErrHumanApprovalRequired = cascade.New(cascade.KindElevationRequired, "jobs: human approval required")

// RequireDistinctFamily checks the eligible families before dispatch. The
// returned flag records a permitted same-family fallback as degraded. This
// checks availability only; completion must check the actual observed family.
// A missing policy fails closed, even if a distinct family is available.
func RequireDistinctFamily(policy ReviewPolicySource, author FamilyRef, eligible []FamilyRef) (degraded bool, err error) {
	for _, candidate := range eligible {
		if familiesDistinct(author, candidate) {
			return familyLadder(policy, true)
		}
	}
	return familyLadder(policy, false)
}

// VerifyObservedFamily checks the family actually selected for the reviewer
// against the author's family at completion. Availability at dispatch does not
// establish observed distinctness. The flag records a permitted degradation.
func VerifyObservedFamily(policy ReviewPolicySource, author, observed FamilyRef) (degraded bool, err error) {
	return familyLadder(policy, familiesDistinct(author, observed))
}

func familiesDistinct(a, b FamilyRef) bool {
	return a.Resolved && b.Resolved && a.Family != "" && b.Family != "" && a.Family != b.Family
}

func familyLadder(policy ReviewPolicySource, distinct bool) (bool, error) {
	if policy == nil {
		return false, ErrNoDistinctFamily
	}
	if distinct {
		return false, nil
	}
	if policy.HoldsWithoutDistinctFamily() {
		return false, ErrNoDistinctFamily
	}
	return policy.RequiresDistinctFamily(), nil
}
