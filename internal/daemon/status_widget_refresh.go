package daemon

// Purpose (this file): the supervised refresh loop behind
// status.widget_changed. Every tick it calls the one emit path
// (emitStatusWidgetChanged), which re-reads providers.db, the node records,
// the unacked attention items (global scope) and the active jobs count, and
// publishes a frame only when the redacted result changed.
//
// Inputs: the run context, the registration's *StatusWidgetDeps, the daemon
// bus, an injected runtime.Ticker (the production one fires every
// StatusWidgetRefreshInterval) and an optional logger.
// Outputs: status.widget_changed frames on the bus, and nil on cancel.
// Constraints: started only through daemonWiring.Manifest.GoSupervised under
// daemonWiring.Ctx (cmd/cascade/wire_status_widget.go, the one caller), never
// with a bare go statement here, so Manifest.Wait joins it before the store
// closes. It exits when ctx ends and stops its ticker. One tick does bounded
// work: a statusWidgetTickBudget deadline over the reads, and a panic in a
// tick is recovered and logged, never ending the loop. A read error keeps the
// last rows and emits nothing (emitStatusWidgetChanged). Ticks coalesce: a
// slow tick never queues more than the ticker's own single pending tick. No
// bare time.Now or time.NewTicker: time comes from deps' injected Clock and
// pacing from the injected Ticker.
//
// SPORT: daemon.status_widget.refresh (ADD, P1-WID-08).

import (
	"context"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// StatusWidgetRefreshInterval is the production refresh period (10 s).
const StatusWidgetRefreshInterval = 10 * time.Second

// StatusWidgetRefreshSubsystem is the manifest name the loop runs under.
const StatusWidgetRefreshSubsystem = "status-widget-refresh"

// statusWidgetTickBudget bounds one tick's reads; it is shorter than the
// refresh interval so a stuck read cannot overlap the next tick.
const statusWidgetTickBudget = 8 * time.Second

// RunStatusWidgetRefresh runs the refresh loop until ctx ends. The first
// frame is published on the first tick that reads successfully (there is no
// start-time pass: a pass at start would race the daemon's own startup and
// publish before anything subscribes). deps and ticker must not be nil; bus
// may be (the loop then keeps reading and publishes nothing); logger may be
// nil.
func RunStatusWidgetRefresh(ctx context.Context, deps *StatusWidgetDeps, bus *events.Bus, ticker runtime.Ticker, logger *slog.Logger) error {
	defer ticker.Stop()
	var emitBus statusWidgetEventBus
	if bus != nil {
		emitBus = bus
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			refreshTick(ctx, deps, emitBus, logger)
		}
	}
}

// refreshTick is one bounded, panic-isolated pass of the emit path.
func refreshTick(ctx context.Context, deps *StatusWidgetDeps, bus statusWidgetEventBus, logger *slog.Logger) {
	defer func() {
		if v := recover(); v != nil && logger != nil {
			logger.Error("status widget refresh tick panicked", slog.Any("panic", v))
		}
	}()
	tickCtx, cancel := context.WithTimeout(ctx, statusWidgetTickBudget)
	defer cancel()
	emitStatusWidgetChanged(tickCtx, deps, bus)
}
