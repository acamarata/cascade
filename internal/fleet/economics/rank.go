// Purpose: pure lane eligibility, role partitioning and six-term utility ranking.
// Inputs: injected request policy, table snapshot and candidate snapshots.
// Outputs: deterministic ordering with verdicts and contribution explanations.
// Constraints: no admission side effects or external reads.

package economics

import (
	"sort"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Rank seals the ordered registry, partitions by requested roles and ranks
// eligible lanes by utility descending, shadow price ascending, then lane ID.
// When no lane matches, valid requested roles fall back to all candidates.
// Jitter is required; the total lane-ID tie-break needs no random draws.
func Rank(req RankRequest, cands []Candidate) (Ranking, error) {
	predicates, terms := laneRankRegistry.seal()
	if req.Jitter == nil {
		return Ranking{}, cascade.New(cascade.KindInvalidInput, "economics: rank requires jitter")
	}
	w, err := req.Tables.Weights(req.Mode)
	if err != nil {
		return Ranking{}, err
	}
	partition, fallback := rankPartition(req.Roles, cands)
	out := Ranking{Order: make([]Ranked, 0, len(partition)), RoleFallback: fallback}
	for _, c := range partition {
		ranked := rankCandidate(req, c, predicates, terms, w)
		out.Order = append(out.Order, ranked)
	}
	sort.SliceStable(out.Order, func(i, j int) bool { return rankBefore(out.Order[i], out.Order[j]) })
	return out, nil
}

func rankPartition(roles []topology.Role, cands []Candidate) ([]Candidate, bool) {
	if len(roles) == 0 {
		return cands, false
	}
	for _, role := range roles {
		if !role.Valid() {
			return nil, false
		}
	}
	var matched []Candidate
	for _, c := range cands {
		if rankRoleMatch(roles, c.Roles) {
			matched = append(matched, c)
		}
	}
	if len(matched) == 0 {
		return cands, true
	}
	return matched, false
}

func rankRoleMatch(wanted, have []topology.Role) bool {
	for _, role := range wanted {
		for _, candidateRole := range have {
			if role == candidateRole && candidateRole.Valid() {
				return true
			}
		}
	}
	return false
}

func rankCandidate(req RankRequest, c Candidate, predicates []Predicate, terms []UtilityTerm, w Weights) Ranked {
	r := Ranked{Candidate: c, Eligible: true, Verdicts: make([]Verdict, 0, len(predicates))}
	for _, p := range predicates {
		v := p.Eval(req, c)
		if p.Name == "mode-permits" && v.Pass {
			if premium := PremiumPermitted(req, c); !premium.Pass {
				v.Pass, v.Applicable, v.Reason = false, true, premium.Reason
			}
		}
		r.Verdicts = append(r.Verdicts, v)
		if v.Applicable && !v.Pass {
			r.Eligible = false
		}
	}
	if !r.Eligible {
		return r
	}
	r.Terms = make([]Term, 0, len(terms))
	for _, term := range terms {
		termReq := req
		if term.Name == "correlation" {
			one := 1.0
			termReq.IndependenceWeight = &one
		}
		v := term.Eval(termReq, c, w)
		if term.Name == "correlation" {
			r.Correlation = v.Value
			independence := DefaultIndependenceWeight(req.Mode, req.Roles)
			if req.IndependenceWeight != nil {
				independence = *req.IndependenceWeight
			}
			v.Value, v.Contribution = v.Value*independence, v.Contribution*independence
		}
		r.Terms = append(r.Terms, v)
		r.Utility += v.Contribution
	}
	return r
}

func rankBefore(a, b Ranked) bool {
	if a.Eligible != b.Eligible {
		return a.Eligible
	}
	if a.Eligible {
		if a.Utility != b.Utility {
			return a.Utility > b.Utility
		}
		if a.Candidate.SP.ShadowPrice != b.Candidate.SP.ShadowPrice {
			return a.Candidate.SP.ShadowPrice < b.Candidate.SP.ShadowPrice
		}
	}
	return a.Candidate.LaneID < b.Candidate.LaneID
}
