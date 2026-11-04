package rpc

// Purpose (this file): the fleet.topology SSE topic (P1-TOP-11): its two
//   event kinds (lane.health_changed, fleet.topology.reconciled), their
//   payload shapes, the filter rule that accepts exactly those two kinds,
//   the owner-only topic access declaration, and a fire-and-forget emitter
//   a topology producer publishes through.
// Inputs: a real internal/events.Bus and the injected runtime.Clock the
//   shared SSEHandler heartbeat reads.
// Outputs: NewFleetTopologySSEHandler, an *SSEHandler bound to the
//   fleet.topology namespace, which the composition root mounts with
//   SSEMux.WithTopic(FleetTopologyTopic, ...). The wire format is the
//   shared SSEHandler envelope (sse.go writeSSEEvent), never a dialect of
//   its own; testdata/fleet_topology_sse.txt is captured from it.
// Constraints: owner-only (AccessOwnerOnly): topology names every lane and
//   account, so no caller principal reads it. FAIL-CLOSED filter: any kind
//   outside the two below, any casing variant and any malformed token is an
//   HTTP 400 before the SSE handshake (sse.go parseFilter). This package
//   never imports internal/fleet/topology: the producer translates its own
//   types into the payloads below.
//
// SPORT: internal.rpc.FleetTopologySSE/ADDED (P1-TOP-11).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// FleetTopologyTopic is the SSEMux topic value that selects this stream.
const FleetTopologyTopic = "fleet.topology"

// fleetTopologyNamespace is the bus namespace fleet.topology events are
// published under; it is distinct from "daemon" so the default stream
// never carries topology events.
const fleetTopologyNamespace = "fleet.topology"

// FleetTopologySchemaVersion is stamped on every fleet.topology payload.
const FleetTopologySchemaVersion = 1

// The fleet.topology event kinds.
const (
	// EventLaneHealthChanged reports one lane moving between health states.
	EventLaneHealthChanged events.EventKind = "lane.health_changed"
	// EventFleetTopologyReconciled reports a finished reconcile pass.
	EventFleetTopologyReconciled events.EventKind = "fleet.topology.reconciled"
)

var _ = DeclareTopicAccess(FleetTopologyTopic, AccessOwnerOnly)

// KnownFleetTopologyEventKind reports whether kind is one of the two
// fleet.topology event kinds. Matching is byte-exact.
func KnownFleetTopologyEventKind(kind events.EventKind) bool {
	return kind == EventLaneHealthChanged || kind == EventFleetTopologyReconciled
}

// LaneHealthChangedPayload is the lane.health_changed payload.
type LaneHealthChangedPayload struct {
	SchemaVersion int    `json:"schema_version"`
	LaneID        string `json:"lane_id"`
	From          string `json:"from"`
	To            string `json:"to"`
	Reason        string `json:"reason,omitempty"`
}

// FleetTopologyReconciledPayload is the fleet.topology.reconciled payload.
type FleetTopologyReconciledPayload struct {
	SchemaVersion int    `json:"schema_version"`
	Generation    uint64 `json:"generation"`
	Lanes         int    `json:"lanes"`
	Changed       int    `json:"changed"`
}

// NewFleetTopologySSEHandler builds the shared SSEHandler bound to the
// fleet.topology namespace with the fleet.topology filter rule.
func NewFleetTopologySSEHandler(bus *events.Bus, clock runtime.Clock) *SSEHandler {
	return NewSSEHandler(bus, fleetTopologyNamespace, KnownFleetTopologyEventKind, clock)
}

// FleetTopologyEventBus is the publish seam the emitter needs;
// *events.Bus satisfies it.
type FleetTopologyEventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// FleetTopologyEmitter publishes fleet.topology events. A nil emitter or
// nil bus makes every Emit call a no-op (daemonless mode).
type FleetTopologyEmitter struct {
	bus FleetTopologyEventBus
}

// NewFleetTopologyEmitter builds an emitter over bus.
func NewFleetTopologyEmitter(bus FleetTopologyEventBus) *FleetTopologyEmitter {
	return &FleetTopologyEmitter{bus: bus}
}

// publish marshals payload and publishes it. Failures are swallowed: the
// stream is observability, not part of the topology change itself.
func (e *FleetTopologyEmitter) publish(ctx context.Context, kind events.EventKind, payload any) {
	if e == nil || e.bus == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = e.bus.Publish(ctx, fleetTopologyNamespace, kind, string(kind), raw)
}

// EmitLaneHealthChanged publishes lane.health_changed.
func (e *FleetTopologyEmitter) EmitLaneHealthChanged(ctx context.Context, p LaneHealthChangedPayload) {
	p.SchemaVersion = FleetTopologySchemaVersion
	e.publish(ctx, EventLaneHealthChanged, p)
}

// EmitReconciled publishes fleet.topology.reconciled.
func (e *FleetTopologyEmitter) EmitReconciled(ctx context.Context, p FleetTopologyReconciledPayload) {
	p.SchemaVersion = FleetTopologySchemaVersion
	e.publish(ctx, EventFleetTopologyReconciled, p)
}
