package daemon

// Purpose (this file): status.widget_changed's SSE half - the event kind,
// the "daemon" namespace choice, and the ONE emit path every trigger goes
// through, with content dedup. Split out of status_widget.go under the repo's
// 300-line file cap.
//
// NAMESPACE (recorded, not guessed - the exact trap S40-T4 hit and this
// ticket's own brief names by name). cmd/cascade/compose_daemon.go's
// newDaemonEventsMux binds the daemon's one real GET /events SSEHandler to
// namespace "daemon" only (events.Bus.Subscribe fans in one namespace, never
// across namespaces - internal/rpc/sse.go's own package doc).
// internal/fleet/capacity/rpc.go's fleet.capacity_changed publishes under
// "fleet" instead and is therefore invisible to that one real subscriber
// today - a pre-existing defect in a different ticket's files_scope, found
// and recorded there, not fixed here. statusWidgetNamespace below is
// "daemon", the one proven-reachable convention.
//
// TRIGGERS (P1-WID-08). There are two, both calling emitStatusWidgetChanged:
// the supervised refresh tick (status_widget_refresh.go, every 10 s, which
// re-reads providers.db, node records, unacked attention and active jobs,
// so a change made by another process or another Store is picked up), and
// the synchronous push through deps' own attention Store
// (attentionForwardBus). The old Compositor onChange trigger is retired: it
// fired from inside every handler pull and from inside the Compositor's lock,
// so one change could publish from three places. Dedup (status_widget_key.go)
// makes any extra call a no-op: a frame is published only when the redacted
// snapshot minus seq, generated_at and updated_at differs from the last frame
// published, and seq moves by one per published frame.
//
// SPORT: daemon.status_widget.sse (ADD, P1-E38-W8-S74-T1; CHANGE, P1-WID-08).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
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
// mechanism this file's TRIGGERS note describes. Publish's
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

// emitStatusWidgetChanged is the single emit path. It re-reads the sources,
// composes and redacts the snapshot, and publishes it to bus under
// statusWidgetNamespace with seq+1 - but only when its change key differs
// from the last published frame's. A read that failed (stale rows), a
// compose error, a marshal error or a Publish failure publishes nothing and
// records nothing (so the next call tries again); SSE is observability, not a
// structural invariant of whichever real change triggered it, which is why
// none of these is surfaced. The first call after start always publishes
// (there is no earlier key).
//
// ctx is the daemon run context. emitMu serialises concurrent callers (the
// tick and an attention push), so seq and the key move together.
func emitStatusWidgetChanged(ctx context.Context, deps *StatusWidgetDeps, bus statusWidgetEventBus) {
	if deps == nil || bus == nil {
		return
	}
	deps.emitMu.Lock()
	defer deps.emitMu.Unlock()

	fleetSnap, stale, err := deps.capacitySnapshot(ctx)
	if err != nil || stale {
		return
	}
	snap, err := deps.composeFrom(ctx, fleetSnap, defaultWidgetScope())
	if err != nil {
		return
	}
	next := deps.seq.Load() + 1
	snap.Seq = next
	redacted := redactSnapshot(deps, snap)
	key, err := widgetChangeKey(redacted)
	if err != nil || bytes.Equal(key, deps.lastKey) {
		return
	}
	raw, err := json.Marshal(redacted)
	if err != nil {
		return
	}
	if _, err := bus.Publish(ctx, statusWidgetNamespace, StatusWidgetChangedKind, "status.widget", raw); err != nil {
		return
	}
	deps.seq.Store(next)
	deps.lastKey = key
}
