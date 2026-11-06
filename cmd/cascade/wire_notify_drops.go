//go:build !windows

// Purpose: registers the dropped-notification surface over the daemon's
//
//	one notify.Service: a drop sink that publishes notify.dropped on the
//	event bus, and the owner-only notify.stats RPC.
//
// Inputs: w.Deps.Notify (the "notify" registration, Order 5), w.Bus,
//
//	w.Registry, w.Manifest and w.Ctx.
//
// Outputs: the "notify-drops" registration; notify.dropped events on the
//
//	"notify" namespace carrying {id, source, priority, reason} only.
//
// Constraints: the sink never blocks a producer: it hands the event to a
//
//	bounded queue and a supervised publisher writes the bus; a full queue
//	skips the event (the drop is still counted and in the Inbox). No body or
//	payload leaves the Service. No second notify.Service.
//
// SPORT: cmd/cascade daemon registrations — notify-drops (P1-HRN-04).
package main

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/notify"
)

const (
	// notifyDroppedKind is the bus event kind of one dropped notification.
	notifyDroppedKind events.EventKind = "notify.dropped"
	// notifyDropsSubsystem names the supervised notify.dropped publisher.
	notifyDropsSubsystem = "notify.drops"
	// notifyDropsBuffer bounds the events waiting for the publisher.
	notifyDropsBuffer = 256
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "notify-drops", Phase: phaseSupervised, Order: 6,
	Wire: wireNotifyDrops,
})

// notifyDroppedPayload is the notify.dropped event body: identifiers and the
// reason only.
type notifyDroppedPayload struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Priority string `json:"priority"`
	Reason   string `json:"reason"`
}

// wireNotifyDrops installs the bus drop sink and registers notify.stats.
func wireNotifyDrops(w *daemonWiring) error {
	if w.Deps.Notify == nil {
		return nil
	}
	svc := w.Deps.Notify.Service()
	if w.Bus != nil {
		queue := make(chan notify.DropEvent, notifyDropsBuffer)
		if err := svc.SetDropSink(queueDropSink(queue)); err != nil {
			return err
		}
		bus := w.Bus
		w.Manifest.GoSupervised(w.Ctx, notifyDropsSubsystem, "publishing notify.dropped",
			func(ctx context.Context) error { return publishDrops(ctx, bus, queue) })
	}
	return notify.RegisterStatsRPC(w.Registry, svc)
}

// queueDropSink returns a DropSink that hands each event to queue without
// blocking; an event that finds queue full is skipped.
func queueDropSink(queue chan<- notify.DropEvent) notify.DropSink {
	return func(_ context.Context, e notify.DropEvent) {
		select {
		case queue <- e:
		default:
		}
	}
}

// dropPublisher is the bus surface publishDrops writes to.
type dropPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// publishDrops publishes each queued DropEvent as notify.dropped until ctx
// ends. A publish error is not fatal: the drop is already counted.
func publishDrops(ctx context.Context, bus dropPublisher, queue <-chan notify.DropEvent) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-queue:
			payload, err := json.Marshal(notifyDroppedPayload{
				ID: e.ID, Source: e.Source, Priority: e.Priority.String(), Reason: e.Reason,
			})
			if err != nil {
				continue
			}
			_, _ = bus.Publish(ctx, daemon.NotifyNamespace, notifyDroppedKind, "notify", payload)
		}
	}
}
