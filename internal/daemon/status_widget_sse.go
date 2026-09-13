package daemon

// Purpose (this file): status.widget_changed's SSE half — the event kind,
// the "daemon" namespace choice, and the two real triggers (a Compositor
// tick, an attention-queue push/ack) — split out of status_widget.go
// under the repo's 300-line file cap.
//
// NAMESPACE (recorded, not guessed — the exact trap S40-T4 hit and this
// ticket's own brief names by name). cmd/cascade/daemon_unix_run.go's
// buildRPCServer binds the daemon's one real GET /events SSEHandler to
// namespace "daemon" only (events.Bus.Subscribe fans in one namespace,
// never across namespaces — internal/rpc/sse.go's own package doc).
// internal/fleet/capacity/rpc.go's fleet.capacity_changed publishes under
// "fleet" instead and is therefore invisible to that one real subscriber
// today — a pre-existing defect in a different ticket's files_scope
// (S-63.T1), found and recorded here, not fixed here (see this ticket's
// journal). statusWidgetNamespace below is "daemon", the one proven-
// reachable convention internal/rpc/supervisor_sse.go's own NAMESPACE
// note already documents and cmd/cascade/daemon_unix_reload.go's
// busEventPublisher already uses.
//
// ATTENTION-PUSH TRIGGER (disclosed, not papered over). Emitting on an
// attention-queue push ideally means subscribing to the CANONICAL
// supervision.Store's own "fleet.attention.changed" events (also
// published under a non-"daemon" namespace, the identical class of bug
// as fleet.capacity's above) — but internal/daemon/attention_rpc.go's own
// CONTRACT DEVIATION note already discloses that no daemon-lifetime ctx
// exists anywhere in this composition path for such a background
// Subscribe loop, and that store is private to that file (never
// returned, so this file cannot share it). RegisterStatusWidgetHandler
// therefore constructs its OWN *supervision.Store over the SAME
// underlying provider.Store — a second reader over the same durable
// data, exactly attention_rpc.go's own "not a second journal" precedent
// (daemon_unix_run_fleetjobs.go's header comment) — wired with
// attentionForwardBus, which re-publishes every real emit synchronously
// (Store.Push/Ack call s.emit before returning, no goroutine needed) into
// this ticket's own onPush hook. This makes the trigger real and
// synchronous for any Push/Ack issued through THIS Store instance; a
// fleet.attention.ack call issued through attention_rpc.go's own,
// separate Store instance changes the same underlying data (so the next
// status.widget poll always reflects it) but does not itself fire this
// SSE event — a narrower, disclosed instance of the same "no shared
// Store" limitation, not a new one this ticket invented.
//
// SPORT: daemon.status_widget.sse (ADD, P1-E38-W8-S74-T1).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/supervision"
)

// statusWidgetNamespace is the bus namespace status.widget_changed
// publishes under — see this file's NAMESPACE note above.
const statusWidgetNamespace = "daemon"

// StatusWidgetChangedKind is the status.widget_changed SSE event kind.
const StatusWidgetChangedKind events.EventKind = "status.widget_changed"

// KnownStatusWidgetEventKind is the KnownEventKind predicate the
// composition root combines into buildRPCServer's knownEventKind, exactly
// as KnownSupervisorEventKind/KnownJobLeaseEventKind already are.
func KnownStatusWidgetEventKind(kind events.EventKind) bool {
	return kind == StatusWidgetChangedKind
}

// statusWidgetEventBus is the minimal seam this file publishes through,
// duck-typed against *events.Bus's own Publish signature — matching
// internal/fleet/capacity.EventBus's identical precedent.
type statusWidgetEventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// attentionForwardBus wraps a real supervision.EventBus (possibly nil)
// and additionally invokes onPush synchronously on every Publish — the
// mechanism this file's ATTENTION-PUSH TRIGGER note describes. Publish's
// own real-bus forwarding is preserved unchanged; onPush never affects
// its return value.
type attentionForwardBus struct {
	real   supervision.EventBus
	onPush func(ctx context.Context)
}

// Publish implements supervision.EventBus.
func (f *attentionForwardBus) Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	var ev events.Event
	var err error
	if f.real != nil {
		ev, err = f.real.Publish(ctx, namespace, kind, source, payload)
	}
	if f.onPush != nil {
		f.onPush(ctx)
	}
	return ev, err
}

// emitStatusWidgetChanged recomputes deps' snapshot, redacts it, and
// publishes it to bus under statusWidgetNamespace with seq. A marshal or
// Publish failure, or a recompute error, is swallowed deliberately — SSE
// is observability, not a structural invariant of whichever real event
// (a Compositor tick, an attention push) triggered it, mirroring
// internal/fleet/capacity.WireSSE's identical fire-and-forget rationale.
//
// preSnap, when non-nil, is used AS-IS instead of calling
// deps.capacitySnapshot — mandatory for the Compositor-tick trigger
// (status_widget.go's SetOnChange registration), which runs while the
// Compositor's own mutex is already held; see capacitySnapshot's MUTEX
// NOTE for the deadlock this avoids. The attention-push trigger has no
// such constraint and passes nil.
func emitStatusWidgetChanged(ctx context.Context, deps *StatusWidgetDeps, bus statusWidgetEventBus, preSnap *capacity.FleetSnapshot) {
	if deps == nil || bus == nil {
		return
	}
	var fleetSnap capacity.FleetSnapshot
	if preSnap != nil {
		fleetSnap = *preSnap
	} else {
		var err error
		fleetSnap, err = deps.capacitySnapshot(ctx)
		if err != nil {
			return
		}
	}

	seq := deps.seq.Add(1)
	snap, err := deps.composeFrom(ctx, fleetSnap, defaultWidgetScope())
	if err != nil {
		return
	}
	snap.Seq = seq
	redacted := redactSnapshot(deps, snap)
	raw, err := json.Marshal(redacted)
	if err != nil {
		return
	}
	_, _ = bus.Publish(ctx, statusWidgetNamespace, StatusWidgetChangedKind, "status.widget", raw)
}
