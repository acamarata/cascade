//go:build windows

// Purpose: the Windows counterpart of daemon_unix.go. Every verb refuses
//
//	immediately via internal/daemon's Windows build (lifecycle_windows.go)
//	— this file only adapts those empty-Options calls and converts the
//	result into daemon.go's shared statusView; there is no config load, no
//	process spawn, no socket, because none of those exist on this platform
//	(06-FORGE-SPEC §2 tier-2).
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates; R-14.117 sibling
//
//	split of daemon.go for the unix/windows platform divide).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon"
)

func platformDaemonRun(ctx context.Context, _ daemonDeps) error {
	return daemon.Run(ctx, daemon.RunOptions{})
}

func platformDaemonStart(ctx context.Context, _ daemonDeps) (statusView, error) {
	_, err := daemon.Start(ctx, daemon.StartOptions{})
	return statusView{}, err
}

func platformDaemonStop(ctx context.Context, _ daemonDeps) (statusView, error) {
	_, err := daemon.Stop(ctx, daemon.StopOptions{})
	return statusView{}, err
}

func platformDaemonRestart(ctx context.Context, _ daemonDeps) (statusView, error) {
	_, err := daemon.Restart(ctx, daemon.RestartOptions{})
	return statusView{}, err
}

func platformDaemonStatus(ctx context.Context, _ daemonDeps) (statusView, error) {
	_, err := daemon.Status(ctx, daemon.StatusOptions{})
	return statusView{}, err
}
