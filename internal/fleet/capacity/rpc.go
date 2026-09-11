// Purpose (this file): the fleet.capacity JSON-RPC 2.0 method and the
// fleet.capacity_changed SSE mirror. Mirrors internal/conversation's
// adapter.go+sse.go precedent: a generic *rpc.Registry.Register call (no
// method-specific code lands in internal/rpc itself - see this file's
// CONTRACT DEVIATION note on files_scope), and the SSE half publishes
// through the daemon's real internal/events.Bus under a dedicated
// namespace/kind, which internal/rpc/sse.go's existing generic SSEHandler
// already bridges to subscribed HTTP clients (no bespoke SSE emitter
// needed - see that handler's KnownEventKind seam).
//
// Inputs: an EventBus (nil is a legitimate no-SSE configuration, e.g. an
// embedded/daemonless mode - mirrors conversation.EventBus's identical
// precedent) and a *Compositor.
// Outputs: the fleet.capacity result, or a typed error when comp is nil
// (daemon not yet initialised); a published events.Event on each material
// Diff.
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over). The ticket
// names internal/rpc/registry.go and internal/rpc/sse.go in
// files_scope.change. Neither file needs editing: Registry.Register
// (registry.go) is already a generic, method-name-agnostic call -
// internal/fleet/sessions and internal/conversation both register their
// own methods through it with zero changes to registry.go itself - and
// internal/rpc/sse.go's SSEHandler already bridges ANY events.Bus
// namespace/kind pair a KnownEventKind predicate admits; the daemon
// composition root (out of this ticket's files_scope, same as
// conversation/adapter.go's own identical deviation) is where that
// predicate widens to admit "fleet.capacity_changed", not this file. This
// is the same class of contradiction AGENT-BRIEF.md's BLOCKED-P1-E38 note
// found for internal/daemon/server.go: a files_scope list written before
// the real registration pattern landed.
//
// SPORT: fleet.capacity.rpc (ADD, per T-1 sport_updates).

package capacity

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodFleetCapacity is the fleet.capacity JSON-RPC 2.0 method name.
const MethodFleetCapacity = "fleet.capacity"

// FleetCapacityChangedKind and fleetCapacityNamespace are the
// fleet.capacity_changed SSE topic: namespace "fleet", event
// "fleet.capacity_changed" - matching R-21.272's ratified single-namespace
// shape internal/fleet/sessions and internal/conversation both follow.
const (
	fleetCapacityNamespace                    = "fleet"
	FleetCapacityChangedKind events.EventKind = "fleet.capacity_changed"
)

// EventBus is the minimal seam WireSSE publishes through, duck-typed
// against *events.Bus's own Publish signature - matching
// internal/conversation.EventBus's identical precedent so this package
// never requires importing a concrete Bus construction path in tests.
type EventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// Handler returns the fleet.capacity rpc.HandlerFunc bound to comp. A nil
// comp returns a typed KindUnavailable error on every call - the
// task-spec error path ("daemon not yet ready -> typed error response")
// - rather than panicking or returning a zero-value snapshot that would
// read as "confirmed empty".
func Handler(comp *Compositor) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		if comp == nil {
			return nil, cascade.New(cascade.KindUnavailable, "fleet.capacity: daemon capacity compositor not yet initialised")
		}
		if err := ctx.Err(); err != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, err, "fleet.capacity: context canceled")
		}
		return comp.Snapshot(), nil
	}
}

// RegisterHandlers binds MethodFleetCapacity on reg. See this file's
// CONTRACT DEVIATION note for why registry.go itself needs no change.
func RegisterHandlers(reg *rpc.Registry, comp *Compositor) {
	reg.Register(MethodFleetCapacity, Handler(comp))
}

// changedPayload is fleet.capacity_changed's wire shape: the delta plus
// its monotonic sequence number, per task 5 ("data = JSON-encoded delta +
// monotonic sequence number").
type changedPayload struct {
	Delta *Delta `json:"delta"`
	Seq   uint64 `json:"seq"`
}

// WireSSE installs a ChangeFunc on comp that publishes every material
// Diff to bus under fleetCapacityNamespace/FleetCapacityChangedKind. A nil
// bus makes this a documented no-op (embedded/daemonless mode); Publish
// errors are swallowed with the same fire-and-forget rationale
// internal/fleet/sessions' Store.emit documents (SSE is observability, not
// a structural invariant of fleet.capacity - unlike CLIENT-LOCAL ECHO's
// chat.append_turn, no caller is blocked on this event being delivered).
func WireSSE(comp *Compositor, bus EventBus) {
	if comp == nil {
		return
	}
	comp.SetOnChange(func(_ FleetSnapshot, delta *Delta, seq uint64) {
		if bus == nil {
			return
		}
		raw, err := json.Marshal(changedPayload{Delta: delta, Seq: seq})
		if err != nil {
			return
		}
		_, _ = bus.Publish(context.Background(), fleetCapacityNamespace, FleetCapacityChangedKind, "fleet.capacity", raw)
	})
}
