// Purpose: the per-lane sensitivity ceiling seam. A LaneTierCap names the
//   most sensitive tier one lane may carry and why; the fail-closed admit
//   helper is what the sensitivity filter and the dispatch door call.
// Inputs: a LaneTierCap supplied at construction via WithLaneTierCap.
// Outputs: LaneTierCap, WithLaneTierCap and admitLaneTierCap.
// Constraints: fail closed. A nil cap, a cap error or an empty reason never
//   admits. There is no exported admit-all cap in non-test code; routing does
//   not consult the cap yet (P1-TOP-02f wires it into the filter).
// SPORT: conductor.lane-tiercap/ADD (P1-TOP-02c).

package conductor

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// LaneTierCap reports the most sensitive tier lane may carry, a non-empty
// reason naming where that ceiling came from, or an error. Any error or an
// empty reason means the lane is not admitted.
type LaneTierCap func(ctx context.Context, lane LaneID, lt LaneType) (provider.SensitivityTier, string, error)

// ErrLaneTierCapMissing is the KindInvalidInput refusal NewDaemonRouter
// returns when no LaneTierCap was supplied.
var ErrLaneTierCapMissing = cascade.New(cascade.KindInvalidInput, "conductor.router: no LaneTierCap supplied (WithLaneTierCap is required)")

// WithLaneTierCap is the RouterOption that hands the per-lane sensitivity
// ceiling to a router. A nil cap counts as absent.
func WithLaneTierCap(c LaneTierCap) RouterOption {
	return func(r *DefaultRouter) {
		if c != nil {
			r.laneTierCap = c
		}
	}
}

// admitLaneTierCap reports whether lane (of type lt) may carry req under
// c. It admits only when c returns no error, a non-empty reason and a tier
// at least as restrictive as req (JoinSensitivity(req, tier) == tier). When
// it refuses, flag is "ceiling:<tier>:removed:<lane>(<reason>)".
func admitLaneTierCap(ctx context.Context, c LaneTierCap, lane LaneID, lt LaneType, req provider.SensitivityTier) (bool, string) {
	removed := func(reason string) (bool, string) {
		return false, fmt.Sprintf("ceiling:%s:removed:%s(%s)", req, lane, reason)
	}
	if c == nil {
		return removed("no-cap")
	}
	tier, reason, err := c(ctx, lane, lt)
	if err != nil {
		return removed("cap-error")
	}
	if reason == "" {
		return removed("no-reason")
	}
	if provider.JoinSensitivity(req, tier) != tier {
		return removed(reason)
	}
	return true, ""
}
