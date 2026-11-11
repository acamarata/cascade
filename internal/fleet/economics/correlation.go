// Purpose: R-21.33 structural correlation and learned-pair floors.
// Inputs: producer identity, provider and session, task class, candidate,
// learned pair lookup and verification policy.
// Outputs: a correlation penalty in [0, 1].
// Constraints: identity comes from topology; no discovery or external reads.

package economics

import (
	"math"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// CorrelationPenalty returns the structural correlation cell, optionally
// replaced by a learned value bounded by relationship floors. Verification
// and adversary candidates also retain the structural cell as a floor.
func CorrelationPenalty(producer *topology.ModelIdentity, producerProvider, producerSession, taskClass string, c Candidate, learned LearnedPairsFn, verify bool) float64 {
	if producer == nil {
		return 0
	}
	if producer.Family == "" || c.Identity.Family == "" {
		return 1
	}
	if producer.CanonicalID == "" || c.Identity.CanonicalID == "" {
		return 0.75
	}
	if producerSession != "" && producerSession == string(c.LaneID) {
		return 1
	}
	cell, floor := correlationCell(*producer, producerProvider, c)
	if learned == nil {
		return cell
	}
	value, ok := learned(*producer, producerProvider, c.Identity, c.Provider, taskClass, verify)
	if !ok {
		return cell
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	if verify || correlationAdversary(c.Roles) {
		floor = math.Max(floor, cell)
	}
	return math.Min(1, math.Max(floor, value))
}

// correlationCell resolves model, family and provider relationships in order.
func correlationCell(producer topology.ModelIdentity, provider string, c Candidate) (cell, floor float64) {
	sameProvider := provider == "" || c.Provider == "" || provider == c.Provider
	if producer.CanonicalID == c.Identity.CanonicalID {
		if sameProvider {
			return 0.90, 0.80
		}
		return 0.70, 0.80
	}
	if producer.SameFamily(c.Identity) {
		return 0.75, 0.50
	}
	if sameProvider {
		return 0.50, 0.35
	}
	return 0.30, 0.15
}

// correlationAdversary fails closed for an unrecognised role.
func correlationAdversary(roles []topology.Role) bool {
	for _, role := range roles {
		if role == topology.RoleAdversary || !role.Valid() {
			return true
		}
	}
	return false
}
