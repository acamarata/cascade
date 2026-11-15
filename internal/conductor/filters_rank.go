// Purpose: the Router's final pick. After the privacy, relay-origin,
//   capability, sensitivity and health filters, the RankFn orders the
//   surviving lanes and the quota stage takes the first ranked lane its
//   AdmitLane admits. A configured spill_order partitions that ranked list:
//   each listed lane is its own partition and the first listed eligible
//   lane's partition goes first; an unconfigured order is one partition.
// Inputs: the request, the filtered candidates and the RankFn set by
//   WithRanker (or NewRouter's lane-name default).
// Outputs: the picked candidate plus its rank:<lane>:<utility> explain
//   flag when WithRanker supplied the RankFn.
// Constraints: no fallback pick. A missing RankFn, a RankFn error, an
//   empty ranking or a ranking that names a lane the filters did not
//   leave is ErrNoLane before any lane is admitted.

package conductor

import (
	"context"
	"fmt"
	"slices"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RankFn orders the lanes the Router's hard filters left for req. It
// returns the lanes best first and one utility note per returned lane
// (same index); the Router explains its pick as rank:<lane>:<utility>.
// A lane it leaves out of the ranking is never picked.
type RankFn func(ctx context.Context, req provider.ModelRequest, cands []LaneID) ([]LaneID, []string, error)

// ErrRankerMissing is the KindInvalidInput refusal NewDaemonRouter
// returns when no WithRanker option (or a nil RankFn) was supplied.
var ErrRankerMissing = cascade.New(cascade.KindInvalidInput, "conductor.router: no RankFn supplied (WithRanker is required)")

// WithRanker sets the RankFn that makes the Router's final pick. A nil
// RankFn leaves the router refusing every request with ErrNoLane.
func WithRanker(rank RankFn) RouterOption {
	return func(r *DefaultRouter) {
		r.rank = rank
		r.rankExplicit = true
	}
}

// laneNameRank is the RankFn NewRouter installs when no WithRanker is
// given: it keeps the candidates in the router's own lane-name order, the
// order the pick used before ranking existed. This is not a fallback
// pick: this order IS the ranking, and the quota stage still admits from
// it exactly as it does from any other RankFn's order.
func laneNameRank(_ context.Context, _ provider.ModelRequest, cands []LaneID) ([]LaneID, []string, error) {
	out := make([]LaneID, len(cands))
	copy(out, cands)
	return out, nil, nil
}

// spillOrderLister is the optional capability a spiller exposes to list
// its configured spill order, so the quota stage can bar every listed
// lane the ranked candidates do not hold. A spiller without it keeps
// today's excluded set exactly.
type spillOrderLister interface {
	listSpillOrder() []LaneID
}

// listSpillOrder returns a copy of p's configured spill order.
func (p *QuotaPolicy) listSpillOrder() []LaneID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.order)
}

// filterRank ranks cands, hands the ranked list to the quota stage (which
// admits the first ranked lane its ceiling or spill order allows) and
// records the rank flag for the pick.
func (r *DefaultRouter) filterRank(ctx context.Context, req provider.ModelRequest, allLanes, cands []laneCandidate, exclude, flags []string) (laneCandidate, []string, error) {
	ranked, notes, err := r.rankCandidates(ctx, req, cands)
	if err != nil {
		return laneCandidate{}, flags, err
	}
	cand, flags, err := filterQuota(ctx, r.quota, allLanes, ranked, exclude, flags)
	if err != nil {
		return laneCandidate{}, flags, err
	}
	if r.rankExplicit {
		flags = append(flags, fmt.Sprintf("rank:%s:%s", cand.lane.LaneName, notes[cand.lane.LaneName]))
	}
	return cand, flags, nil
}

// rankCandidates calls the RankFn and maps its order back onto cands. It
// fails closed with ErrNoLane on a missing RankFn, a RankFn error, an
// empty ranking, a lane outside cands, a repeated lane, or (for a RankFn
// set by WithRanker) a utility list that does not match the ranking.
func (r *DefaultRouter) rankCandidates(ctx context.Context, req provider.ModelRequest, cands []laneCandidate) ([]laneCandidate, map[string]string, error) {
	if r.rank == nil {
		return nil, nil, ErrNoLane
	}
	ids := make([]LaneID, len(cands))
	byName := make(map[string]laneCandidate, len(cands))
	for i, c := range cands {
		ids[i] = LaneID(c.lane.LaneName)
		byName[c.lane.LaneName] = c
	}
	order, utilities, err := r.rank(ctx, req, ids)
	if err != nil || len(order) == 0 {
		return nil, nil, ErrNoLane
	}
	if r.rankExplicit && len(utilities) != len(order) {
		return nil, nil, ErrNoLane
	}
	out := make([]laneCandidate, 0, len(order))
	notes := make(map[string]string, len(order))
	for i, id := range order {
		c, ok := byName[string(id)]
		if _, dup := notes[string(id)]; !ok || dup {
			return nil, nil, ErrNoLane
		}
		notes[string(id)] = ""
		if i < len(utilities) {
			notes[string(id)] = utilities[i]
		}
		out = append(out, c)
	}
	return out, notes, nil
}
