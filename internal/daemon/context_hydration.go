package daemon

// Purpose: registers context.hydration.degraded (P1-E16-W4-S34-T4) — the
//   one RPC method the prompt-hydration hook calls when it could not
//   produce a capsule, so the degradation lands on the daemon's own event
//   bus rather than requiring the hook to open a database the daemon
//   already holds.
// Inputs: the daemon's shared *rpc.Registry and *events.Bus.
// Outputs: one published event per accepted call.
// Constraints: no new persistence interface (R-16.6a) — this publishes
//   onto internal/events' Bus and nothing else. The method reports a
//   refusal like any other, but the CALLER treats every failure as a
//   no-op: this is telemetry about a path that already failed open, and
//   failing loudly about failing quietly helps nobody.
// SPORT: internal/daemon (ADD, context.hydration.degraded) — P1-E16-W4-S34-T4.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ContextHydrationDegradedMethod is the JSON-RPC method the hook dials.
const ContextHydrationDegradedMethod = "context.hydration.degraded"

// ContextHydrationDegradedParams is the method's wire params.
//
// Reason only. The hook knows nothing else worth recording — not the
// prompt, which is the user's, and not the cwd, which would put a
// filesystem path into an event log for a counting check that never reads
// it.
type ContextHydrationDegradedParams struct {
	Reason string `json:"reason"`
}

// ContextHydrationDegradedResult echoes the published event's sequence, so
// a caller that wants to can tell a recorded degradation from a dropped
// one. The hook does not look.
type ContextHydrationDegradedResult struct {
	Seq uint64 `json:"seq"`
}

// RegisterContextHydrationHandler binds the method against bus.
//
// A nil bus registers NOTHING rather than a handler that always fails:
// the hook's own fallback is to publish directly, and a method that
// answers with a refusal would make it stop trying.
func RegisterContextHydrationHandler(registry *rpc.Registry, bus *events.Bus) {
	if bus == nil {
		return
	}
	registry.Register(ContextHydrationDegradedMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p ContextHydrationDegradedParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "context.hydration.degraded: decode params")
			}
		}
		if p.Reason == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "context.hydration.degraded: reason must not be empty")
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindInternal, err, "context.hydration.degraded: encode payload")
		}
		published, err := bus.Publish(ctx, hydration.DegradedNamespace, hydration.DegradedKind,
			hydration.DegradedSource, payload)
		if err != nil {
			return nil, err
		}
		return ContextHydrationDegradedResult{Seq: published.Seq}, nil
	})
}
