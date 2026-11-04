// Purpose: NotificationRouter — subscribes to the C/S-04.T3 event bus for
//
//	the notification-class event Kind, decodes each event per the
//	normative source-Kind mapping table, and enqueues into the
//	per-Priority buffered channels dispatch.go drains. The router is
//	strictly internal: it never sends outbound traffic itself, and
//	holds no knowledge of any specific bridge or harness.
//
// Inputs: a live *events.Bus subscription (Run) and, per event, its
//
//	decoded NotificationPayload.
//
// Outputs: a Notification enqueued into queueSet by Priority; an unmapped
//
//	event Kind or an undecodable payload is dropped with a slog WARN,
//	never a panic, and the router keeps running.
//
// Constraints: no bare time.Now (Timestamp comes from the injected
//
//	clock); the router never logs a decoded payload's Body — only the
//	event Kind, id, and error text — so a payload that happened to
//	carry a secret is never written to the log.
//
// SPORT: internal.notify.NotificationRouter/ADDED (P1-E23-W5-S49-T1).

package notify

import (
	"context"
	"log/slog"
	"sort"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// sourceMapping is one normative source-Kind mapping table row: the Class
// and Priority a bus-decoded event of a given EventKind resolves to.
type sourceMapping struct {
	class    Class
	priority Priority
}

// routerTrustedOrigins maps the bus Kinds whose Origin claims trust to their
// Origin.Source name in TrustedProducers. Every other Kind's Origin is the
// Kind itself and untrusted-source.
var routerTrustedOrigins = map[events.EventKind]string{
	"supervision.stalled": "supervision.stall",
}

// sourceKindMapping is the ticket's normative source-Kind mapping table.
// A Kind absent from this map is dropped with a slog WARN — never
// silently defaulted to a class or priority.
var sourceKindMapping = map[events.EventKind]sourceMapping{
	"attention.raised":      {ClassScoped, PriorityHigh},
	"supervision.stalled":   {ClassScoped, PriorityUrgent},
	"backup.result":         {ClassScoped, PriorityNormal},
	"conversation.event":    {ClassScoped, PriorityNormal},
	"provider.auth.expired": {ClassGlobalCritical, PriorityUrgent},
	"node.offline":          {ClassGlobalCritical, PriorityUrgent},
	"disk.nearly.full":      {ClassGlobalCritical, PriorityHigh},
	"elevation.required":    {ClassGlobalCritical, PriorityUrgent},
}

// GlobalCriticalSources returns a sorted copy of the router's closed global-critical sources.
func GlobalCriticalSources() []string {
	var sources []string
	for source, mapping := range sourceKindMapping {
		if mapping.class == ClassGlobalCritical {
			sources = append(sources, string(source))
		}
	}
	sort.Strings(sources)
	return sources
}

// NotificationRouter decodes notification-class events off an
// events.Subscription and enqueues them for dispatch.
//
// P1-E23-W5-S49-T2 CHANGE: the router exposes away-mode accumulation
// (SetAccumulate / DrainAccumulated) but does NOT own it — the buffer and
// its gate live on queueSet (dispatch.go), the single choke point every
// producer reaches, so Notifier.Deliver's direct producer path is gated by
// exactly the same switch as a bus-decoded event. These methods are the
// away controller's handle on that gate.
type NotificationRouter struct {
	queues *queueSet
	clock  runtime.Clock
	log    *slog.Logger
}

// NewNotificationRouter returns a NotificationRouter enqueuing into
// queues, timestamping decoded Notifications with clock.
func NewNotificationRouter(queues *queueSet, clock runtime.Clock, log *slog.Logger) *NotificationRouter {
	return &NotificationRouter{queues: queues, clock: clock, log: log}
}

// Run reads sub.Events until it closes, ctx is canceled, or sub.Errs
// delivers a fatal subscription error (returned as-is). Every
// EventKindNotification event is decoded and enqueued; every other Kind
// received on this subscription, and every EventKindNotification event
// whose payload fails to decode, is dropped with a slog WARN.
func (r *NotificationRouter) Run(ctx context.Context, sub *events.Subscription) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-sub.Errs:
			if ok && err != nil {
				return err
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			r.handle(ev)
		}
	}
}

// handle decodes and enqueues one bus event.
func (r *NotificationRouter) handle(ev events.Event) {
	mapping, ok := sourceKindMapping[ev.Kind]
	if !ok {
		r.log.Warn("notify: dropping event with unmapped kind", "kind", string(ev.Kind))
		return
	}
	payload, err := events.DecodeNotificationPayload(ev.Payload)
	if err != nil {
		r.log.Warn("notify: dropping malformed notification payload", "kind", string(ev.Kind))
		return
	}
	n := Notification{
		ID:            payload.ID,
		Source:        string(ev.Kind),
		Priority:      mapping.priority,
		Class:         mapping.class,
		DeepLink:      payload.DeepLink,
		OriginScope:   payload.OriginScope,
		TargetScope:   payload.TargetScope,
		TargetSession: payload.TargetSession,
		TargetTask:    payload.TargetTask,
		Visibility:    Visibility(payload.Visibility),
		CorrelationID: payload.CorrelationID,
		Payload:       payload.Body,
		Timestamp:     r.clock.Now(),
		Origin:        routerOrigin(ev.Kind, payload.ID),
	}
	stamp(&n)
	r.queues.enqueue(n)
}

// SetAccumulate switches every producer between immediate fan-out (false)
// and away-mode accumulation (true). Switching from true to false does NOT
// itself flush the buffer — DrainAccumulated is the only way accumulated
// notifications leave it, so a caller that flips accumulation off without
// draining first has them waiting for the next explicit drain.
func (r *NotificationRouter) SetAccumulate(accumulate bool) {
	r.queues.setAccumulate(accumulate)
}

// Accumulating reports whether notifications are currently being buffered
// instead of queued for dispatch.
func (r *NotificationRouter) Accumulating() bool {
	return r.queues.isAccumulating()
}

// DrainAccumulated atomically returns every notification buffered while
// accumulation was on, in priority order (Urgent, High, Normal, Low;
// stable within a priority so arrival order is preserved), and clears the
// buffer. It does not change the accumulation flag itself.
func (r *NotificationRouter) DrainAccumulated() []Notification {
	return r.queues.drainAccumulated()
}

// reclassifyAccumulated sets Priority to Urgent on every still-buffered
// notification whose ID or CorrelationID equals matchID and returns how
// many it changed. Package-private: only away.go's stall-escalation path
// calls it, and it reports 0 rather than pretending a match.
func (r *NotificationRouter) reclassifyAccumulated(matchID string) int {
	return r.queues.reclassifyAccumulated(matchID)
}

// requeue returns n to the NORMAL per-priority queues, bypassing the
// accumulation gate. digest.go calls it for every drained original no
// delivered digest represented (no-silent-discard): re-queued items keep
// their own priority, class, scope and expiry and are gated by
// ScopeDeliveryPredicate again at the next ordinary Drain, never
// re-widened. Bypassing the gate is the point — a re-queue must land in
// the queue it is being returned to, not back into the buffer it just left.
func (r *NotificationRouter) requeue(n Notification) {
	r.queues.enqueueNow(n)
}

// routerOrigin is the Origin of a bus-decoded event: an internal-tier record
// named by the payload id, trusted only for a routerTrustedOrigins Kind.
func routerOrigin(kind events.EventKind, id string) Origin {
	o := Origin{Source: string(kind), RecordRef: id, Tier: provider.SensitivityInternal,
		Trust: provider.ProvenanceUntrustedSource}
	if src, ok := routerTrustedOrigins[kind]; ok {
		o.Source, o.Trust = src, provider.ProvenanceTrusted
	}
	return o
}
