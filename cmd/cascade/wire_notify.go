//go:build !windows

// Purpose: registers the notification runtime: the daemon's one
//
//	notify.Service, its dispatcher drain and its bus router.
//
// Inputs: the daemonWiring's run context, Manifest, Bus, Clock and Logger.
// Outputs: w.Deps.Notify, read by every later registration.
// Constraints: notify.NewService is called here and nowhere else in the
//
//	daemon, exactly once per composition. Both loops start (in the last
//	registration, after all migrations) through
//	Manifest.GoSupervised with w.Ctx, so daemon stop joins them before the
//	bus and store close; the drain ticker is created inside its goroutine.
//
// SPORT: cmd/cascade daemon registrations — notify (P1-AGT-04).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "notify", Phase: phaseSupervised, Order: 5,
	Wire: func(w *daemonWiring) error {
		wireNotifyRuntime(w)
		return nil
	},
})

// The loops start after every other registration has wired: the router
// subscribes to the event bus from its head and records its cursor in the
// shared database, which must not overlap the schema migrations the
// supervised and later registrations run on that database (SQLITE_BUSY).
var _ = registerDaemonWiring(daemonRegistration{
	Name: "notify-loops", Phase: phaseEventTopics, Order: 1000,
	Wire: func(w *daemonWiring) error {
		startNotifyLoops(w, func() runtime.Ticker { return runtime.NewSystemTicker(daemon.NotifyDrainInterval) })
		return nil
	},
})

// newNotifyService is the one constructor call the notify registration
// makes; a test counts calls through it.
var newNotifyService = notify.NewService

// wireNotifyRuntime builds the notification runtime and publishes it on w.Deps.
func wireNotifyRuntime(w *daemonWiring) {
	w.Deps.Notify = daemon.NewNotifyRuntime(newNotifyService(notify.DefaultConfig(), w.Clock, w.Logger))
}

// startNotifyLoops starts the drain (paced by newTicker) and the router as
// supervised loops over the runtime wireNotifyRuntime published.
func startNotifyLoops(w *daemonWiring, newTicker func() runtime.Ticker) {
	rt := w.Deps.Notify
	w.Manifest.GoSupervised(w.Ctx, daemon.NotifyDispatcherSubsystem, "draining notification queues",
		func(ctx context.Context) error { return rt.RunDispatcher(ctx, newTicker()) })
	w.Manifest.GoSupervised(w.Ctx, daemon.NotifyRouterSubsystem, "routing the notify namespace",
		func(ctx context.Context) error { return rt.RunRouter(ctx, w.Bus) })
}
