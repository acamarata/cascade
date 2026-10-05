// Purpose: the lane write behind the outcome decorator: read the lane's
//
//	current row, change its State and ResetEstimate to what one call's
//	evidence classified, and write it back through registry.UpsertLane.
//
// Inputs: the lookup the Resolver already holds (widened with UpsertLane),
//
//	the lane name, the injected clock and a logger.
//
// Outputs: at most one UpsertLane per outcome. Nothing is returned: a
//
//	recorder failure is logged and never changes the call's result.
//
// Constraints: last-observed semantics. A write happens only when State or
//
//	ResetEstimate would change (compared at the millisecond the registry
//	stores); every other lane field is written back exactly as read. The
//	read-modify-write is serialised within this process by the Resolver's
//	mutex, but a 401 from a call made with an old key can still land
//	after a verified reauth and re-amber the row, and `exhausted` stays
//	until the next call after its reset passes (docs/provider-guide.md
//	section Lane state). The write also carries back the PoolIndex it
//	read; that is latent while nothing in production advances the pool
//	index. The log line names the lane and the error only; the recorder
//	never holds a request header, URL or body.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
)

// laneRecorder turns one call's observation into a lane write.
type laneRecorder struct {
	lookup RegistryLookup
	lane   string
	clock  runtime.Clock
	log    *slog.Logger
	mu     *sync.Mutex
}

// record classifies the call's evidence and writes the lane if it says to.
// The write runs on a context detached from the call's cancellation, so a
// call that finishes just as its caller gives up still records.
func (r *laneRecorder) record(ctx context.Context, slot *observation, err error) {
	status, headers := slot.snapshot()
	state, reset, write := classifyOutcome(err, status, headers, r.clock.Now())
	if !write {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if werr := r.apply(context.WithoutCancel(ctx), state, reset); werr != nil {
		r.log.Warn("dispatch: lane outcome not recorded", slog.String("lane", r.lane), slog.String("error", werr.Error()))
	}
}

// apply reads the lane, and writes it back with state and reset when
// either differs from what is stored.
func (r *laneRecorder) apply(ctx context.Context, state registry.LaneState, reset time.Time) error {
	lanes, err := r.lookup.ListLanes(ctx)
	if err != nil {
		return err
	}
	for _, lane := range lanes {
		if lane.LaneName != r.lane {
			continue
		}
		if lane.State == state && sameResetEstimate(lane.ResetEstimate, reset) {
			return nil
		}
		lane.State, lane.ResetEstimate = state, reset
		return r.lookup.UpsertLane(ctx, lane)
	}
	return nil
}

// sameResetEstimate compares two estimates at the millisecond the registry
// stores, treating the zero time as "none".
func sameResetEstimate(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && b.IsZero()
	}
	return a.UnixMilli() == b.UnixMilli()
}
