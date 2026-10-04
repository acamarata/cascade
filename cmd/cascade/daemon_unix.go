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
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// composeDaemon builds everything the daemon needs before it serves. This is
// the production composition root: it opens the real on-disk Store every other
// subsystem shares, runs the crash-recovery scan with a real DomainRegistry,
// and hands daemon.Run a real *http.Server (POST /rpc, GET /events) and, for a
// released build, a real UpgradeManager. The cleanups it returns run in
// reverse order, also when it returns an error.
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

	bus := events.New(store, deps.Clock)
	if err := runRecoveryScan(ctx, paths, settings, deps, logProvider, bus, store); err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	// M/S-27.T2: crash/upgrade-in-place resume over the same store, before any
	// RPC connection (daemon_resume.go names the disclosed Executor gap).
	if _, err := wireResumeScan(ctx, store, deps.Clock); err != nil {
		return daemon.RunOptions{}, cleanups, err
	}

	memoryAdmin, pol, cleanupBackground, err := wireBackgroundSubsystems(ctx, paths, deps, cfg, store, rawDB, bus, logProvider)
	if err != nil {
		return daemon.RunOptions{}, cleanups, err
	}
	cleanups = append(cleanups, cleanupBackground)
	wireCascadePAInstallHostDeps(store, pol.Queue, pol.Registry) // P1-E24-W5-S50-T4 (D1/D2)
	plugins.SetBridgeApprovalQueue(pol.Queue)                    // P1-E23-W5-S48-T4 FIX-0

	// withNodePlacement hands the placement engine its connection source: the
	// controller-side tunnel registry this process holds (P1-E17-W4-S37-T1).
	server, manifest, connections, err := buildRPCServer(bus, deps.Clock, logProvider.Logger(), settings, paths, memoryAdmin, store,
		withPolicyHandlers(pol), withStatusWidgetHandler(store, deps.Clock, bus, paths, cfg.Widget.ShowProjectNames),
		withNodePlacement(deps.NodeTunnels), withDaemonRuntime(cfg, rawDB, pol, logProvider, deps), withRegistryObserver(observe))
	if err != nil {
		return daemon.RunOptions{}, cleanups, err
	}

	opts := daemon.RunOptions{
		Settings:    settings,
		PIDPath:     daemon.PIDFilePath(paths),
		Logger:      logProvider.Logger(),
		Clock:       deps.Clock,
		Server:      server,
		Environ:     deps.Environ,
		Manifest:    manifest,
		Connections: connections,
	}
	wireUpgrade(&opts, deps, store, bus, logProvider.Logger())
	return opts, cleanups, nil
}

// platformDaemonRun composes the daemon (composeDaemon) and serves it
// (daemon.Run), running every cleanup composeDaemon registered, in reverse
// order, whether it returned an error or not.
func platformDaemonRun(ctx context.Context, deps daemonDeps) error {
	opts, cleanups, err := composeDaemon(ctx, deps, nil)
	defer runCleanupsLIFO(cleanups)
	if err != nil {
		return err
	}
	return daemon.Run(ctx, opts)
}

// runCleanupsLIFO runs cleanups in reverse registration order, the order the
// deferred calls they replace would have run in.
func runCleanupsLIFO(cleanups []func()) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// wireUpgrade sets opts.Upgrade/Executable/Args from a real UpgradeManager,
// but ONLY when this binary carries a real, released build hash
// (daemon.BuildHash() != "dev"). An unreleased build's hash is always
// "dev" (upgrade.go's own doc comment), which never matches any on-disk
// binary, so CheckSkew reports skew against itself unconditionally. That
// is documented as "expected and harmless... since Relaunch only runs
// when a caller acts on CheckSkew's result", true only as long as nothing
// production-side ever DOES act on it. Wiring RunOptions.Upgrade
// unconditionally breaks that assumption: every SIGTERM/SIGINT on a dev
// build would attempt drain-and-exec-relaunch instead of a clean exit,
// which is exactly the daemon-never-stops regression the SIGTERM/SIGKILL
// round-trip test (daemon_test.go) caught during development (dev builds
// are the only kind this repo's own test suite and CI ever run). This
// guard keeps the real UpgradeManager reachable from the real Run path
// while keeping a dev build's ordinary shutdown exactly as clean as it
// was before this file wired Upgrade in, since Upgrade nil is Run's
// documented "skip upgrade, drain and exit normally" path.
func wireUpgrade(opts *daemon.RunOptions, deps daemonDeps, store provider.Store, bus *events.Bus, logger *slog.Logger) {
	if daemon.BuildHash() == "dev" {
		return
	}
	opts.Upgrade = daemon.NewUpgradeManager(deps.Clock, time.Sleep, store, bus, logger)
	opts.Executable = deps.Executable
	opts.Args = relaunchExecArgs(deps)
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

func stopOptions(paths runtime.PathProvider, settings daemon.Settings) daemon.StopOptions {
	return daemon.StopOptions{
		PIDPath:    daemon.PIDFilePath(paths),
		Prober:     daemon.NewProber(),
		Signal:     daemon.DefaultSignal,
		SocketGone: func() bool { _, err := os.Stat(settings.SocketPath); return os.IsNotExist(err) },
		Sleep:      time.Sleep,
	}
}

// socketDialable reports whether some process is currently accepting
// connections at path.
func socketDialable(path string) bool {
	c, err := net.Dial("unix", path)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func startDetail(res daemon.StartResult) string {
	if res.AlreadyRunning {
		return "already running pid=" + strconv.Itoa(res.PID)
	}
	return "started pid=" + strconv.Itoa(res.PID)
}

func stopDetail(res daemon.StopResult) string {
	switch {
	case !res.WasRunning:
		return "not running"
	case res.Escalated:
		return "stopped (escalated to SIGKILL)"
	default:
		return "stopped"
	}
}
