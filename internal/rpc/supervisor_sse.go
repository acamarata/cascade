package rpc

// Purpose (this file): the four supervisor.* SSE event kinds, their wire
// payload shapes (each carrying schema_version per this ticket's
// instruction), and SupervisorEventEmitter, the small publisher every real
// producer (attention pushes, stall detection, escalation, headroom
// republish) calls through.
//
// NAMESPACE (recorded, not guessed). The daemon's one real, wired SSE
// endpoint (cmd/cascade/daemon_unix_run.go's buildRPCServer) constructs its
// SSEHandler as `rpc.NewSSEHandler(bus, "daemon", knownEventKind, clock)` —
// events.Bus.Subscribe subscribes to exactly that one namespace string, so
// an event published under any OTHER namespace ("fleet", "fleet.attention",
// "jobs.lease", ...) is invisible to it. internal/rpc/sse_jobs.go's own
// header already discloses this exact gap for job/lease events ("...to the
// jobs.lease bus namespace, not whichever single namespace a daemon's
// SSEHandler binds to"), and cmd/cascade/daemon_unix_reload.go's
// busEventPublisher already publishes under the literal "daemon" namespace
// for precisely this reason. supervisorEventNamespace below follows that
// same, already-proven-reachable convention rather than inventing a
// "supervisor" namespace nothing would ever subscribe to.
//
// KNOWN-KIND REGISTRATION. Combines into buildRPCServer's knownEventKind
// via CombineKnownEventKind (sse_jobs.go's own combinator), mirroring
// KnownJobLeaseEventKind exactly.
//
// SPORT: supervisor.sse_events/ADDED (P1-E18-W4-S40-T4).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
)

// supervisorEventNamespace is the bus namespace supervisor.* events
// publish under — see this file's NAMESPACE note above.
const supervisorEventNamespace = "daemon"

// The four supervisor.* SSE event kinds this ticket adds.
const (
	EventSupervisorAttentionAdded events.EventKind = "supervisor.attention_added"
	EventSupervisorStallDetected  events.EventKind = "supervisor.stall_detected"
	EventSupervisorEscalation     events.EventKind = "supervisor.escalation"
	EventSupervisorHeadroomUpdate events.EventKind = "supervisor.headroom_update"
)

// supervisorEventKinds lists the four constants above, for
// KnownSupervisorEventKind.
var supervisorEventKinds = []events.EventKind{
	EventSupervisorAttentionAdded,
	EventSupervisorStallDetected,
	EventSupervisorEscalation,
	EventSupervisorHeadroomUpdate,
}

// KnownSupervisorEventKind reports whether kind is one of the four
// registered supervisor.* kinds — a KnownEventKind predicate the
// composition root combines with its own via CombineKnownEventKind
// (sse_jobs.go), exactly as KnownJobLeaseEventKind already does for job/
// lease events.
func KnownSupervisorEventKind(kind events.EventKind) bool {
	for _, k := range supervisorEventKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// SupervisorAttentionAddedPayload is supervisor.attention_added's payload:
// one item entered the attention queue (internal/fleet/supervision.Push).
type SupervisorAttentionAddedPayload struct {
	SchemaVersion int    `json:"schema_version"`
	ItemID        string `json:"item_id"`
	Kind          string `json:"kind"`
	ScopeKind     string `json:"scope_kind"`
	ScopeID       string `json:"scope_id"`
}

// SupervisorStallDetectedPayload is supervisor.stall_detected's payload: a
// session's ProgressTracker concluded ProgressStalled.
type SupervisorStallDetectedPayload struct {
	SchemaVersion int    `json:"schema_version"`
	SessionID     string `json:"session_id"`
	StallKind     string `json:"stall_kind"`
}

// SupervisorEscalationPayload is supervisor.escalation's payload: the
// governor escalation ladder advanced a rung for a session.
type SupervisorEscalationPayload struct {
	SchemaVersion int    `json:"schema_version"`
	SessionID     string `json:"session_id"`
	Rung          string `json:"rung"`
}

// SupervisorHeadroomUpdatePayload is supervisor.headroom_update's payload:
// a fresh internal/fleet.Headroom reading (S-40.T2).
type SupervisorHeadroomUpdatePayload struct {
	SchemaVersion   int     `json:"schema_version"`
	Resource        string  `json:"resource"`
	EnforcedCeiling int64   `json:"enforced_ceiling"`
	Ratio           float64 `json:"ratio"`
}

// SupervisorEventBus is the minimal seam SupervisorEventEmitter publishes
// through, duck-typed against *events.Bus's own Publish signature —
// matching internal/fleet/capacity's and internal/fleet/supervision's own
// identical EventBus precedent, so this file never requires a live bus in
// tests.
type SupervisorEventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// SupervisorEventEmitter publishes the four supervisor.* SSE events. The
// zero value is not usable; construct with NewSupervisorEventEmitter.
//
// WIRING GAP (disclosed, not papered over). A production emitter needs a
// call site inside each real producer: internal/fleet/supervision's
// Store.Push/stallSupervisor.CreateSupervisor (attention_added/
// stall_detected/escalation) and internal/fleet.HeadroomPublisher.publish
// (headroom_update). None of those files are in this ticket's files_scope,
// and two independent, larger gaps already block wiring them from the
// daemon composition root today: (1) no *runtime.Registry (the C-S05.T4
// metrics registry) is constructed anywhere in cmd/cascade's production
// path, so HeadroomPublisher itself is never started in production either
// (grep confirms zero callers of fleet.NewHeadroomPublisher outside
// internal/fleet's own tests); (2) buildRPCServer's own attention_rpc.go
// precedent already discloses that no daemon-lifetime context is threaded
// through it for a subsystem's background goroutine to run against. Both
// are real, disclosed, pre-existing gaps this ticket did not introduce and
// cannot close within files_scope. This type, its four Emit* methods, and
// their wire shapes above are the ready, tested destination for whichever
// change closes either gap — exactly the class of situation
// internal/rpc/sse_jobs.go's own header already accepted for job/lease
// events ("Wiring a real producer... is left as an explicit, named gap").
type SupervisorEventEmitter struct {
	bus SupervisorEventBus
}

// NewSupervisorEventEmitter builds an emitter over bus. A nil bus makes
// every Emit* call a documented no-op (embedded/daemonless mode), matching
// internal/fleet/capacity.WireSSE's identical nil-bus convention.
func NewSupervisorEventEmitter(bus SupervisorEventBus) *SupervisorEventEmitter {
	return &SupervisorEventEmitter{bus: bus}
}

// publish is the shared fire-and-forget publish path every Emit* method
// uses. A marshal or Publish failure is swallowed deliberately: SSE is
// observability, not a structural invariant of whichever real operation
// (a push, a stall detection, an escalation, a headroom tick) triggered
// the event — mirroring internal/fleet/supervision.Store.emit's identical
// rationale.
func (e *SupervisorEventEmitter) publish(ctx context.Context, kind events.EventKind, source string, payload any) {
	if e == nil || e.bus == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = e.bus.Publish(ctx, supervisorEventNamespace, kind, source, raw)
}

// EmitAttentionAdded publishes supervisor.attention_added.
func (e *SupervisorEventEmitter) EmitAttentionAdded(ctx context.Context, p SupervisorAttentionAddedPayload) {
	p.SchemaVersion = SupervisorSchemaVersion
	e.publish(ctx, EventSupervisorAttentionAdded, "supervisor.attention_added", p)
}

// EmitStallDetected publishes supervisor.stall_detected.
func (e *SupervisorEventEmitter) EmitStallDetected(ctx context.Context, p SupervisorStallDetectedPayload) {
	p.SchemaVersion = SupervisorSchemaVersion
	e.publish(ctx, EventSupervisorStallDetected, "supervisor.stall_detected", p)
}

// EmitEscalation publishes supervisor.escalation.
func (e *SupervisorEventEmitter) EmitEscalation(ctx context.Context, p SupervisorEscalationPayload) {
	p.SchemaVersion = SupervisorSchemaVersion
	e.publish(ctx, EventSupervisorEscalation, "supervisor.escalation", p)
}

// EmitHeadroomUpdate publishes supervisor.headroom_update.
func (e *SupervisorEventEmitter) EmitHeadroomUpdate(ctx context.Context, p SupervisorHeadroomUpdatePayload) {
	p.SchemaVersion = SupervisorSchemaVersion
	e.publish(ctx, EventSupervisorHeadroomUpdate, "supervisor.headroom_update", p)
}
