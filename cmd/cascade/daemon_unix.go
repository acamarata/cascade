//go:build !windows

// Purpose: the real (non-Windows) implementations behind daemon.go's
//
//	platformDaemon{Run,Start,Stop,Restart,Status} call sites — assembling
//	internal/daemon's Options structs from the resolved config, path
//	provider, and production ProcessProber/Spawn/Signal implementations.
//
// Inputs: the same daemonDeps daemon.go's cobra RunE closures pass through.
// Outputs: internal/daemon's *Result types (Run returns only an error);
//
//	Status converts to the shared statusView.
//
// Constraints: this is cmd/'s composition root for the daemon subsystem
//
//	(Art.10.2) — internal/daemon takes every dependency by injection, so
//	ALL environment access (os.Executable, time.Sleep, the log file path)
//	is resolved here, once, and passed down.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates; R-14.117 sibling
//
//	split of daemon.go for the unix/windows platform divide).
package main

import (
	"context"
	"os"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// composeDaemon builds everything the daemon needs before it serves. This is
// the production composition root: it opens the real on-disk Store every other
// subsystem shares, runs the crash-recovery scan with a real DomainRegistry,
// and hands daemon.Run a real *http.Server (POST /rpc, GET /events) and, for a
// released build, a real UpgradeManager. The cleanups it returns run in
// reverse order, also when it returns an error.
//
// It derives the daemon's one run context (runCtx) from ctx and creates the
// one Manifest before any background subsystem starts. Every supervised
// goroutine runs under runCtx and registers on that Manifest, and every
// registration reads runCtx as daemonWiring.Ctx (withRunContext). The join
// cleanup (cancel runCtx, then Manifest.Wait) is appended after closeStore's,
// the bus close's and cleanupBackground's, so it runs before all three on
// every exit path.
//
// It calls runtime.Scan directly rather than runtime.Bootstrap: Bootstrap
// resolves its own PathProvider from Getenv/HomeDir, which would silently stop
// honoring deps.Paths, the injection seam for "where is CASCADE_HOME" that
// every platformDaemon* function and test shares (see docs/developer/
// runtime-bootstrap.md).
func composeDaemon(ctx context.Context, deps daemonDeps, observe registryObserver) (daemon.RunOptions, []func(), error) {
	var cleanups []func()
	cfg, paths, settings, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	logProvider, err := runtime.NewLogProvider(cfg.Logging, paths, deps.Clock)
	if err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	cleanups = append(cleanups, func() { _ = logProvider.Close() })

	store, rawDB, closeStore, err := openRuntimeStore(ctx, paths, deps.Clock)
	if err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	cleanups = append(cleanups, closeStore)

	// The bus closes after the join and the background cleanup and before
	// the store: Close stops every delivery goroutine a subscriber left
	// running (they read the store), so none outlives it.
	bus := events.New(store, deps.Clock)
	cleanups = append(cleanups, func() { _ = bus.Close() })
	if err := runRecoveryScan(ctx, paths, settings, deps, logProvider, bus, store); err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	// M/S-27.T2: resume scan before any RPC connection; dispatches nothing
	// (fan-out cursors: the fanout-resume registration, wire_resume.go).
	if _, err := wireResumeScan(ctx, store, deps.Clock); err != nil {
		return daemon.RunOptions{}, cleanups, err
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	manifest := daemon.NewManifest(logProvider.Logger(), deps.Clock)
	joinRun := joinRunContext(cancelRun, manifest)
	memoryAdmin, pol, cleanupBackground, err := wireBackgroundSubsystems(runCtx, manifest, paths, deps, cfg, store, rawDB, bus, logProvider)
	if err != nil {
		joinRun()
		return daemon.RunOptions{}, cleanups, err
	}
	cleanups = append(cleanups, cleanupBackground, joinRun)
	wireCascadePAInstallHostDeps(store, pol.Queue, pol.Registry) // P1-E24-W5-S50-T4 (D1/D2)
	plugins.SetBridgeApprovalQueue(pol.Queue)                    // P1-E23-W5-S48-T4 FIX-0

	// withNodePlacement hands the placement engine its connection source: the
	// controller-side tunnel registry this process holds (P1-E17-W4-S37-T1).
	server, _, connections, err := buildRPCServer(bus, deps.Clock, logProvider.Logger(), settings, paths, memoryAdmin, store,
		withRunContext(runCtx), withManifest(manifest),
		withPolicyHandlers(pol), withStatusWidgetHandler(store, deps.Clock, bus, paths, cfg.Widget.ShowProjectNames),
		withNodePlacement(deps.NodeTunnels), withDaemonRuntime(cfg, rawDB, pol, logProvider, deps), withRegistryObserver(observe))
	if err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	opts := daemon.RunOptions{
		Settings: settings, PIDPath: daemon.PIDFilePath(paths), Logger: logProvider.Logger(), Clock: deps.Clock,
		Server: server, Environ: deps.Environ, Manifest: manifest, Connections: connections,
	}
	wireUpgrade(&opts, deps, store, bus, manifest.RelaunchJoin(cancelRun, settings.ShutdownGrace))
	return opts, cleanups, nil
}

// platformDaemonRun composes the daemon (composeDaemon) and serves it
// (daemon.Run), running every cleanup composeDaemon registered, in reverse
// order, whether it returned an error or not. Run returning for any reason
// (ctx cancelled, a termination signal, an upgrade attempt that returned)
// reaches the same cleanups, so the run context is cancelled and every
// supervised goroutine joined before the background cleanup and the store
// close run.
func platformDaemonRun(ctx context.Context, deps daemonDeps) error {
	opts, cleanups, err := composeDaemon(ctx, deps, nil)
	defer runCleanupsLIFO(cleanups)
	if err != nil {
		return err
	}
	return daemon.Run(ctx, opts)
}

// platformDaemonStart starts the daemon in the background, idempotently.
func platformDaemonStart(ctx context.Context, deps daemonDeps) (statusView, error) {
	_, paths, settings, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return statusView{}, err
	}
	execPath, err := deps.Executable()
	if err != nil {
		return statusView{}, err
	}
	if err := ensureSpawnDirs(paths); err != nil {
		return statusView{}, err
	}
	res, err := daemon.Start(ctx, daemon.StartOptions{
		PIDPath:    daemon.PIDFilePath(paths),
		Prober:     daemon.NewProber(),
		Spawn:      daemon.DefaultSpawn(execPath, relaunchArgs(), runtime.LogFilePath(paths)),
		ReadyProbe: func() bool { return socketDialable(settings.SocketPath) },
		Sleep:      time.Sleep,
	})
	if err != nil {
		return statusView{}, err
	}
	return statusView{Running: true, PID: res.PID, Detail: startDetail(res)}, nil
}

// ensureSpawnDirs creates the directories DefaultSpawn's own log file
// (opened in the PARENT process before the relaunched child ever runs
// internal/daemon.Run — which creates its own pidfile/socket directories
// once it starts) needs to exist first. `cascade daemon start` against a
// CASCADE_HOME that has never been initialized must not fail on a missing
// directory a real `cascade init` would normally have created.
func ensureSpawnDirs(paths runtime.PathProvider) error {
	if err := os.MkdirAll(paths.LogDir(), 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "create log directory")
	}
	return nil
}

// platformDaemonStop stops the running daemon.
func platformDaemonStop(ctx context.Context, deps daemonDeps) (statusView, error) {
	_, paths, settings, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return statusView{}, err
	}
	res, err := daemon.Stop(ctx, stopOptions(paths, settings))
	if err != nil {
		return statusView{}, err
	}
	return statusView{Detail: stopDetail(res)}, nil
}

// platformDaemonRestart stops then starts the daemon.
func platformDaemonRestart(ctx context.Context, deps daemonDeps) (statusView, error) {
	_, paths, settings, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return statusView{}, err
	}
	execPath, err := deps.Executable()
	if err != nil {
		return statusView{}, err
	}
	if err := ensureSpawnDirs(paths); err != nil {
		return statusView{}, err
	}
	res, err := daemon.Restart(ctx, daemon.RestartOptions{
		Stop: stopOptions(paths, settings),
		Start: daemon.StartOptions{
			PIDPath:    daemon.PIDFilePath(paths),
			Prober:     daemon.NewProber(),
			Spawn:      daemon.DefaultSpawn(execPath, relaunchArgs(), runtime.LogFilePath(paths)),
			ReadyProbe: func() bool { return socketDialable(settings.SocketPath) },
			Sleep:      time.Sleep,
		},
	})
	if err != nil {
		return statusView{}, err
	}
	return statusView{Running: true, PID: res.StartResult.PID, Detail: startDetail(res.StartResult)}, nil
}

// platformDaemonStatus reports whether the daemon is running.
func platformDaemonStatus(ctx context.Context, deps daemonDeps) (statusView, error) {
	_, paths, _, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return statusView{}, err
	}
	res, err := daemon.Status(ctx, daemon.StatusOptions{
		PIDPath: daemon.PIDFilePath(paths),
		Prober:  daemon.NewProber(),
		Clock:   deps.Clock,
	})
	if err != nil {
		return statusView{}, err
	}
	return statusView{
		Running: res.Running, PID: res.PID, UptimeS: res.UptimeS,
		Connections: res.Connections, Detail: res.Detail,
	}, nil
}
