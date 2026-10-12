// Purpose: the daemon's one notification runtime: it holds the single
//
//	notify.Service the composition root builds and runs its two loops, the
//	dispatcher drain and the bus router over the "notify" namespace.
//
// Inputs: the notify.Service, a pacing runtime.Ticker for the drain, and the
//
//	event bus subscribe side for the router.
//
// Outputs: Notifications drained to subscribers and recorded in the Inbox.
// Constraints: neither loop starts a goroutine itself; the composition root
//
//	runs each through Manifest.GoSupervised so daemon stop joins both before
//	the store closes and a panic in one is recorded Failed without stopping
//	the other. A cancelled context ends a loop with a nil error.
//
// SPORT: daemon.notify_runtime (ADD, P1-AGT-04).

package daemon

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
)

// Manifest names and bus coordinates of the notification runtime.
const (
	NotifyDispatcherSubsystem = "notify.dispatcher"
	NotifyRouterSubsystem     = "notify.router"
	NotifyNamespace           = "notify"
	notifyRouterCursor        = "notify-router"
	notifyRouterBuffer        = 64
)

// NotifyDrainInterval is the production pause between two drain cycles.
const NotifyDrainInterval = 250 * time.Millisecond

// NotifySubscriber is the subscribe side of the event bus the router needs.
// *events.Bus satisfies it.
type NotifySubscriber interface {
	SubscribeFromHead(ctx context.Context, namespace, cursorName string, bufferSize int) (*events.Subscription, error)
}

// NotifyRuntime owns the daemon's one notify.Service.
type NotifyRuntime struct {
	svc *notify.Service
}

// NewNotifyRuntime wraps svc, the one Service the composition root built.
func NewNotifyRuntime(svc *notify.Service) *NotifyRuntime {
	return &NotifyRuntime{svc: svc}
}

// Service returns the daemon's one notify.Service.
func (r *NotifyRuntime) Service() *notify.Service { return r.svc }

// RunDispatcher drains the Service's queues once per tick until ctx ends,
// then stops ticker. It returns nil on cancellation.
func (r *NotifyRuntime) RunDispatcher(ctx context.Context, ticker runtime.Ticker) error {
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			r.svc.Dispatcher.Drain(ctx)
		}
	}
}

// RunRouter subscribes to the "notify" namespace from its head and routes
// each notification event into the Service's queues until ctx ends. A
// subscribe failure or a subscription error is returned; cancellation
// returns nil.
func (r *NotifyRuntime) RunRouter(ctx context.Context, bus NotifySubscriber) error {
	sub, err := bus.SubscribeFromHead(ctx, NotifyNamespace, notifyRouterCursor, notifyRouterBuffer)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := r.svc.Router.Run(ctx, sub); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
