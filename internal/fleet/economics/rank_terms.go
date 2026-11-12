// Purpose: R-21.120's six utility terms and default independence policy.
// Inputs: a RankRequest, Candidate and the request's table weight row.
// Outputs: named, weighted contributions to lane utility.
// Constraints: pure arithmetic; no discovery, configuration or I/O.

package economics

import (
	"math"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// DefaultIndependenceWeight returns 1 for verify or adversary work and
// 0.25 otherwise. Unknown modes and roles use the strictest weight.
func DefaultIndependenceWeight(m Mode, roles []topology.Role) float64 {
	if !m.Valid() || m == ModeVerify || correlationAdversary(roles) {
		return 1
	}
	return 0.25
}

// BuiltinUtilityTerms returns the six utility evaluators in formula order.
// Values are raw magnitudes; penalty contributions carry a negative sign.
// The caller supplies the weight row resolved from the request's Tables.
func BuiltinUtilityTerms() []UtilityTerm {
	return []UtilityTerm{
		{Name: "quality", Eval: func(_ RankRequest, c Candidate, w Weights) Term {
			return utilityTerm("quality", w.Quality, c.ExpectedQuality, 1)
		}},
		{Name: "shadow_price", Eval: func(_ RankRequest, c Candidate, w Weights) Term {
			return utilityTerm("shadow_price", w.Scarcity, math.Log1p(c.SP.ShadowPrice), -1)
		}},
		{Name: "correlation", Eval: func(req RankRequest, c Candidate, w Weights) Term {
			independence := DefaultIndependenceWeight(req.Mode, req.Roles)
			if req.IndependenceWeight != nil {
				independence = *req.IndependenceWeight
			}
			// RankRequest has no producer provider; an unknown provider
			// retains CorrelationPenalty's conservative same-provider rule.
			verify := !req.Mode.Valid() || req.Mode == ModeVerify || correlationAdversary(req.Roles)
			correlation := CorrelationPenalty(req.Producer, "", req.ProducerSession, req.TaskClass, c, req.LearnedPairs, verify)
			return utilityTerm("correlation", w.Diversity, correlation*independence, -1)
		}},
		{Name: "latency", Eval: func(_ RankRequest, c Candidate, w Weights) Term {
			return utilityTerm("latency", w.Latency, utilityLatency(c), -1)
		}},
		{Name: "predicted_failure", Eval: func(_ RankRequest, c Candidate, w Weights) Term {
			return utilityTerm("predicted_failure", w.Reliability, c.PredictedFailure, -1)
		}},
		{Name: "risk", Eval: func(_ RankRequest, c Candidate, w Weights) Term {
			return utilityTerm("risk", w.Reliability, 2.0*c.EWMA429+0.20*utilityLatency(c), -1)
		}},
	}
}

// EvaluateUtilityTerms resolves the request's weight row and evaluates all
// six terms. A zero Tables, missing row or unknown mode returns the table's
// KindInvalidInput error, with no partial terms and no default fallback.
func EvaluateUtilityTerms(req RankRequest, c Candidate) ([]Term, error) {
	w, err := req.Tables.Weights(req.Mode)
	if err != nil {
		return nil, err
	}
	terms := BuiltinUtilityTerms()
	out := make([]Term, len(terms))
	for i, term := range terms {
		out[i] = term.Eval(req, c, w)
	}
	return out, nil
}

// utilityTerm keeps the raw weight and value alongside their signed product.
func utilityTerm(name string, weight, value, sign float64) Term {
	return Term{Name: name, Weight: weight, Value: value, Contribution: sign * weight * value}
}

// utilityLatency normalizes median latency to the closed unit interval.
func utilityLatency(c Candidate) float64 {
	return math.Max(0, math.Min(1, float64(c.LatencyP50)/float64(10*time.Second)))
}
