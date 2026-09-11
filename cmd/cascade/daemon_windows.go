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
	"github.com/acamarata/cascade/internal/fleet/resume"
)

// platformDaemonRun reports resume.RefuseDaemonlessResume's typed
// refusal FIRST (M/S-27.T2's Windows tier-2 leg — the daemon this
// package's ResumeManager resumes for does not exist on this platform at
// all), then falls through to internal/daemon's own windows build, which
// refuses every verb identically via lifecycle_windows.go.
func platformDaemonRun(ctx context.Context, _ daemonDeps) error {
	if err := resume.RefuseDaemonlessResume(); err != nil {
		return err
	}
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
