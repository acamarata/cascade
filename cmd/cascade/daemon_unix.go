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
	"net"
	"os"
	"strconv"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// platformDaemonRun runs the daemon in the foreground.
func platformDaemonRun(ctx context.Context, deps daemonDeps) error {
	cfg, paths, settings, err := loadDaemonConfig(ctx, deps)
	if err != nil {
		return err
	}
	logProvider, err := runtime.NewLogProvider(cfg.Logging, paths, deps.Clock)
	if err != nil {
		return err
	}
	defer func() { _ = logProvider.Close() }()

	return daemon.Run(ctx, daemon.RunOptions{
		Settings: settings,
		PIDPath:  daemon.PIDFilePath(paths),
		Logger:   logProvider.Logger(),
		Clock:    deps.Clock,
	})
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
