// Purpose: the relay-origin filter (FILTER 0.5, P1-NODE-06a). When a lane
// dispatch relays work back into the conductor router, the relay stamps a
// RelayOrigin on the context naming the origin dispatch, lane and model.
// This filter restricts the candidate set to exactly the origin lane and
// refuses any candidate that would resolve to a different model. A
// request without a relay origin in context passes through unchanged.
// Inputs: the context (which may carry a RelayOrigin), the current
// candidate list, and the accumulated reason flags.
// Outputs: the filtered candidate list, the updated reason flags, and
// either nil or a cascade.KindUnavailable error when the origin lane is
// missing or the only remaining candidate resolves to a different model.
// Constraints: never dispatches another lane when the origin lane is
// unavailable; the refusal is KindUnavailable "relay lane unavailable".
// The model-match check happens here rather than downstream so a
// relayed call that asks for a different model than its origin is refused
// before any registry read or provider call.
// SPORT: conductor.filter-relay-origin/ADD (P1-NODE-06a).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrRelayLaneUnavailable is the refusal a relayed call gets when its
// origin lane is not in the candidate set, or when the only candidate
// remaining after filtering resolves to a different model than the
// origin. KindUnavailable (not KindConflict) matches the taxonomy: the
// request is well formed, but the lane it must use is not available to
// this selection pass.
var ErrRelayLaneUnavailable = cascade.New(cascade.KindUnavailable,
	"conductor: relay lane unavailable")

// filterRelayOrigin is the relay-origin gate. When ctx carries a
// RelayOrigin it keeps only the candidate whose lane name matches the
// origin LaneID, then refuses if no candidate remains or if the remaining
// candidate's resolved model differs from the origin Model. When ctx
// carries no RelayOrigin it returns cands and flags unchanged. A relayed
// request that named node capabilities is refused first with
// ErrRelayNodePlacement (router_placement.go), before any lane is kept.
//
// The model check uses resolveModel() from filters_dispatch.go rather
// than accessing lane fields directly, since the model resolution logic
// is centralized there.
func filterRelayOrigin(ctx context.Context, cands []laneCandidate, flags []string) ([]laneCandidate, []string, error) {
	o, ok := RelayOriginFrom(ctx)
	if !ok {
		return cands, flags, nil
	}
	if flags, err := refuseRelayPlacement(flags); err != nil {
		return nil, flags, err
	}
	out := make([]laneCandidate, 0, 1)
	for _, c := range cands {
		if c.lane.LaneName == o.LaneID {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, append(flags, "relay:no-origin-lane"), ErrRelayLaneUnavailable
	}
	if resolveModel(out[0]) != o.Model {
		return nil, append(flags, "relay:model-mismatch"), ErrRelayLaneUnavailable
	}
	return out, append(flags, "relay:origin-lane="+o.LaneID), nil
}
