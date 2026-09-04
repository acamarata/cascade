//go:build !windows

// Purpose: the daemon run path's config hot-reload wiring —
//
//	runtime.NewHotReloader plus runtime.NewReloadWatcher, both started
//	against the real store's audit recorder and the real fsnotify watch
//	on config.toml. R-14.175 named this pair as built, tested, and never
//	started by a running daemon; this file is the composition root that
//	starts them. Split from daemon_unix.go under R-14.117 (Art.10.3's
//	300-line cap; daemon_unix.go was already close to it).
//
// Inputs: the *runtime.Config platformDaemonRun already loaded, the
//
//	PathProvider/Clock/Getenv/Environ daemonDeps carries, the real
//	provider.Store openRuntimeStore opened, and the *events.Bus the
//	daemon publishes to.
//
// Outputs: a started *runtime.HotReloader and *runtime.ReloadWatcher.
//
//	The caller (platformDaemonRun) owns stopping the watcher on exit.
//
// Constraints: no bare time.Now (Art.7.3 — Clock flows to both the
//
//	recorder and the reloader); this file's EventPublisher adapter is
//	the same seam-closing pattern runtimeEventBusAdapter already uses in
//	daemon_unix_run.go for runtime.EventBus, applied here to
//	runtime.EventPublisher's different (string-name, map-payload) shape.
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 hot-reload wiring).
package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// hotReloadEventNamespace is the events.Bus namespace hot-reload events
// publish to — the same "daemon" namespace buildRPCServer's SSE handler
// already subscribes, so a future addition to its knownEventKind
// predicate (buildRPCServer's own doc comment) is the only step needed
// to stream these live; today they are still durably journaled via
// events.Bus.Publish regardless of what SSE forwards.
const hotReloadEventNamespace = "daemon"

// hotReloadEventSource identifies this publisher in the event's Source
// field.
const hotReloadEventSource = "runtime.hotreload"

// busEventPublisher adapts *events.Bus's typed, byte-payload Publish to
// runtime.EventPublisher's (name string, payload map[string]interface{})
// shape — the same composition-root adapter pattern
// runtimeEventBusAdapter uses one file over, for the other event
// producer this process has. A JSON-marshal failure or a Publish error is
// swallowed here deliberately: EventPublisher's own contract
// (hotreload_events.go) is fire-and-forget from HotReloader's
// perspective, and ReloadOutcome / the audit record remain the
// authoritative account of what Reload actually did either way.
type busEventPublisher struct {
	bus *events.Bus
}

func (p *busEventPublisher) Publish(ctx context.Context, name string, payload map[string]interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = p.bus.Publish(ctx, hotReloadEventNamespace, events.EventKind(name), hotReloadEventSource, data)
}

// wireHotReload builds the real HotReloader (over store's real
// StoreAuditRecorder — R-14.175's other named gap, closed here in the
// same call rather than as a separate seam) and starts the real
// fsnotify ReloadWatcher on paths.ConfigPath(). The caller stops the
// returned watcher on shutdown.
func wireHotReload(paths runtime.PathProvider, clock runtime.Clock, getenv runtime.Getenv, environ func() []string, cfg *runtime.Config, store provider.Store, bus *events.Bus, logs *runtime.LogProvider) (*runtime.HotReloader, *runtime.ReloadWatcher, error) {
	loadOpts := runtime.LoadOptions{
		Path:    paths.ConfigPath(),
		Getenv:  getenv,
		Environ: environ,
	}
	audit := runtime.NewStoreAuditRecorder(store, clock)
	hr := runtime.NewHotReloader(paths.ConfigPath(), loadOpts, cfg, clock, &busEventPublisher{bus: bus}, audit, logs)

	watcher := runtime.NewReloadWatcher(hr, paths.ConfigPath(), 0)
	if err := watcher.Start(); err != nil {
		return nil, nil, err
	}
	return hr, watcher, nil
}

// wireBackgroundSubsystems wires both hot reload (wireHotReload) and the
// retention scheduler (startScheduler) and folds their independent
// shutdown steps into one cleanup func — split out of platformDaemonRun
// under Art.10.3's 50-line function cap (R-14.175 wiring pushed it past
// the cap; this is a cap-driven split of composition-root glue, not a
// new concern). On a startScheduler failure this also stops the watcher
// it already started, so a caller that gets a non-nil error never needs
// to guess what, if anything, it must still tear down.
func wireBackgroundSubsystems(ctx context.Context, paths runtime.PathProvider, deps daemonDeps, cfg *runtime.Config, store provider.Store, rawDB *sql.DB, bus *events.Bus, logProvider *runtime.LogProvider) (func(), error) {
	_, watcher, err := wireHotReload(paths, deps.Clock, deps.Getenv, deps.Environ, cfg, store, bus, logProvider)
	if err != nil {
		return nil, err
	}

	// schedCtx is deliberately its own cancellation, not ctx directly:
	// daemon.Run does its own SIGTERM/SIGINT handling
	// (internal/daemon/lifecycle_unix.go) rather than reacting to ctx
	// cancellation, so ctx is not guaranteed to be cancelled when Run
	// returns. schedCancel, called from the returned cleanup, is what
	// actually stops the scheduler's tick loop on shutdown.
	schedCtx, schedCancel := context.WithCancel(ctx)
	_, schedCleanup, err := startScheduler(schedCtx, store, rawDB, deps.Clock, bus, logProvider.Logger())
	if err != nil {
		schedCancel()
		watcher.Stop()
		return nil, err
	}

	return func() {
		watcher.Stop()
		schedCancel()
		schedCleanup(context.Background())
	}, nil
}
