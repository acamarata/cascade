//go:build !windows

// Purpose: the daemon run path's supervision and lifecycle helpers: the
//
//	run-context join composeDaemon registers as a cleanup, the upgrade
//	wiring that joins before a relaunch, and the stop/status helpers the
//	other platformDaemon* functions share. Split out of daemon_unix.go to
//	keep it under Art.10.3's 300-line cap.
//
// Inputs: the run context's cancel func, the daemon's one Manifest, and the
//
//	values platformDaemon* already resolved.
//
// Outputs: cleanup funcs, RunOptions upgrade fields, and status strings.
// Constraints: the join cancels first and then waits; it never waits on a
//
//	live run context (Manifest.Wait would block for as long as the daemon
//	runs).
//
// SPORT: cmd/cascade/daemon (CHANGED, run-context join and helper split).
package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// joinRunContext returns composeDaemon's join cleanup: it cancels the run
// context and then blocks until every goroutine supervised on manifest has
// returned. composeDaemon appends it after closeStore and cleanupBackground,
// so (cleanups run last-in first-out) nothing those two release is still in
// use by a background goroutine. A second call is a no-op join.
func joinRunContext(cancelRun context.CancelFunc, manifest *daemon.Manifest) func() {
	return func() {
		cancelRun()
		manifest.Wait()
	}
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
//
// beforeRelaunch becomes UpgradeManager.BeforeRelaunch: composeDaemon passes
// Manifest.RelaunchJoin, so a skew relaunch cancels the run context and
// joins the supervised goroutines (bounded by the drain grace) before exec
// replaces the process image.
func wireUpgrade(opts *daemon.RunOptions, deps daemonDeps, store provider.Store, bus *events.Bus, beforeRelaunch func(context.Context)) {
	if daemon.BuildHash() == "dev" {
		return
	}
	opts.Upgrade = daemon.NewUpgradeManager(deps.Clock, time.Sleep, store, bus, opts.Logger)
	opts.Upgrade.BeforeRelaunch = beforeRelaunch
	opts.Executable = deps.Executable
	opts.Args = relaunchExecArgs(deps)
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
