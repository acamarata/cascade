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

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// sourceMapping is one normative source-Kind mapping table row: the Class
// and Priority a bus-decoded event of a given EventKind resolves to.
type sourceMapping struct {
	class    Class
	priority Priority
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

// NotificationRouter decodes notification-class events off an
// events.Subscription and enqueues them for dispatch.
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
	}
	r.queues.enqueue(n)
}
