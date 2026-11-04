// Purpose: ExplorationEligible, the gate that decides whether a request
//
//	may be routed to a candidate lane for exploration (P1-TOP-02a).
//
// Inputs: the request tier, the candidate's Ceiling and offering policy,
//
//	and the candidate and incumbent risk tolerances.
//
// Outputs: a bool. Constraints: only public requests explore; an unknown
//
//	policy term, a ceiling that does not admit the request or a candidate
//	riskier than the incumbent (or a NaN tolerance) never explores.
//
// SPORT: fleet/topology/exploration/ADD (P1-TOP-02a).

package topology

import "github.com/acamarata/cascade/pkg/provider"

// ExplorationEligible reports whether a request at tier req may explore
// candidate cand: req is public, cand admits it, policy declares its
// training and retention terms, and candTol <= incTol.
func ExplorationEligible(req provider.SensitivityTier, cand Ceiling, policy ProviderPolicy, candTol, incTol float64) bool {
	if req != provider.SensitivityPublic || !TierAdmits(req, cand) || !PolicyKnown(policy) {
		return false
	}
	return candTol <= incTol
}
