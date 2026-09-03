//go:build !windows

package daemon

// Purpose: Run — `cascade daemon run`'s foreground implementation on every
//   non-Windows platform (06-FORGE-SPEC §2: unix socket, 0600 perms).
//   Creates the IPC socket, writes the pidfile, serves (accepts and, at
//   this ticket, immediately closes — D/S-06.T3 owns protocol handling)
//   connections while tracking an active-connection count, and drains on
//   SIGTERM/SIGINT within the configured [daemon] shutdown_grace window.
// Inputs: RunOptions — resolved Settings, the pidfile path, an injected
//   *slog.Logger/runtime.Clock, and two test seams: Signals (an injectable
//   os.Signal channel — production wires signal.Notify itself when nil) and
//   Ready (closed once the socket is listening and the pidfile is written,
//   letting a test synchronize on readiness instead of sleeping, R-14.136).
// Outputs: nil on a clean drained shutdown; a typed error on socket/pidfile
//   failure (also recorded in the Manifest as a fail-loud ERROR line).
// Constraints: this file only creates and removes the socket — no
//   JSON-RPC or other protocol handling belongs here (ticket scope). Split
//   across lifecycle_unix_{start,stop,status,prober}.go under R-14.117 to
//   stay under Art.10.3's 300-line file cap; see this ticket's final
//   report for the exact split.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ipcSocketSubsystem is the single subsystem this ticket's Run registers
// (R-14.87: "at W1 minimally the IPC socket listener").
const ipcSocketSubsystem = "ipc-socket"

// RunOptions carries every input Run needs, injected so no test touches a
// real signal, socket path outside t.TempDir(), or unlogged clock (Art.7.1).
type RunOptions struct {
	Settings Settings
	PIDPath  string
	Logger   *slog.Logger
	Clock    runtime.Clock
	// Manifest, if nil, is constructed fresh from Logger/Clock.
	Manifest *Manifest
	// Signals delivers termination signals. A nil channel makes Run
	// install its own signal.Notify(SIGTERM, SIGINT) — production's real
	// path. Tests inject a channel they control directly instead.
	Signals <-chan os.Signal
	// Ready, if non-nil, is closed once the socket is listening and the
	// pidfile is written — a test synchronization point (no sleeps).
	Ready chan<- struct{}
	// Upgrade, if non-nil, is consulted on every termination trigger
	// before Run's normal drain-and-exit: a detected binary-version skew
	// (R-14.12) drains the listener in place and exec-relaunches instead
	// of a plain exit. nil preserves Run's exact pre-upgrade behavior.
	// Executable/Args/Environ are read only when Upgrade is set — see
	// upgrade.go's package doc for why the wiring lives here rather than
	// at the Restart()/CLI composition layer (lifecycle_unix_stop.go and
	// cmd/cascade/daemon_unix.go are outside this ticket's files_scope).
	Upgrade    *UpgradeManager
	Executable func() (string, error)
	Args       func() []string
	Environ    func() []string
}

// Run serves the daemon in the foreground until ctx is canceled or a
// termination signal arrives, then drains and returns.
func Run(ctx context.Context, opts RunOptions) error {
	manifest := opts.Manifest
	if manifest == nil {
		manifest = NewManifest(opts.Logger, opts.Clock)
	}
	manifest.Register(ipcSocketSubsystem)

	ln, cleanup, err := setUpSocketAndPIDFile(opts, manifest)
	if err != nil {
		return err
	}
	defer cleanup()

	manifest.Started(ipcSocketSubsystem, opts.Settings.SocketPath)
	if opts.Ready != nil {
		close(opts.Ready)
	}

	sigs := opts.Signals
	if sigs == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(ch)
		sigs = ch
	}

	var active int64
	acceptDone := make(chan struct{})
	go acceptLoop(ln, opts.Upgrade, opts.Logger, &active, acceptDone)

	select {
	case <-sigs:
	case <-ctx.Done():
	}

	if attemptUpgrade(ctx, opts, ln) {
		// A successful Relaunch never returns to its caller — this line
		// is reachable only when a test's execFunc stub returns. A real
		// successful exec replaces the process image before it gets here.
		return nil
	}

	_ = ln.Close()
	<-acceptDone
	drain(opts, &active)
	return nil
}

// setUpSocketAndPIDFile binds the unix socket and writes the pidfile,
// recording either failure on manifest. cleanup closes/removes both and
// must run via defer regardless of how Run later exits.
func setUpSocketAndPIDFile(opts RunOptions, manifest *Manifest) (net.Listener, func(), error) {
	ln, err := listenSocket(opts.Settings.SocketPath)
	if err != nil {
		manifest.Failed(ipcSocketSubsystem, err.Error())
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: listen socket")
	}
	socketCleanup := func() { _ = ln.Close(); _ = os.Remove(opts.Settings.SocketPath) }

	if err := os.MkdirAll(filepath.Dir(opts.PIDPath), 0o700); err != nil {
		manifest.Failed(ipcSocketSubsystem, "pidfile dir: "+err.Error())
		socketCleanup()
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: create pidfile directory")
	}
	if err := writePIDFile(opts.PIDPath, pidRecord{PID: os.Getpid(), StartedAt: opts.Clock.Now()}); err != nil {
		manifest.Failed(ipcSocketSubsystem, "pidfile: "+err.Error())
		socketCleanup()
		return nil, nil, err
	}
	return ln, func() { _ = removePIDFile(opts.PIDPath); socketCleanup() }, nil
}

// attemptUpgrade consults opts.Upgrade, if set, before Run's ordinary
// drain-and-exit. nil Upgrade/Executable, a CheckSkew error, a "no skew"
// result, or a failed Relaunch all report false so Run falls through to
// its normal shutdown — the non-bricking fallback this ticket requires:
// Run's own deferred cleanup still removes the socket and pidfile.
func attemptUpgrade(ctx context.Context, opts RunOptions, ln net.Listener) bool {
	if opts.Upgrade == nil || opts.Executable == nil {
		return false
	}
	execPath, err := opts.Executable()
	if err != nil {
		if opts.Logger != nil {
			opts.Logger.Warn("daemon: upgrade: resolve executable failed", slog.String("error", err.Error()))
		}
		return false
	}
	args := []string{execPath}
	if opts.Args != nil {
		args = opts.Args()
	}
	var env []string
	if opts.Environ != nil {
		env = opts.Environ()
	}
	relaunched, _ := opts.Upgrade.AttemptUpgrade(ctx, execPath, ln, nil, opts.Settings.ShutdownGrace, args, env)
	return relaunched
}

// listenSocket binds a unix socket at path with 0600 permissions. A stale
// socket file left by a crashed prior daemon (nobody listening) is removed
// and the bind retried once; a socket a live process is actually listening
// on is a genuine conflict, reported as-is.
func listenSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		if isAddrInUse(err) && !dialable(path) {
			_ = os.Remove(path)
			ln, err = net.Listen("unix", path)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

// dialable reports whether some live process is actually accepting
// connections at path (as opposed to a stale socket file left behind by an
// unclean exit).
func dialable(path string) bool {
	c, err := net.Dial("unix", path)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// acceptLoop accepts connections until ln is closed. A connection accepted
// while upgrade is mid-Drain is refused with ErrDraining (logged, closed
// without ever being counted) rather than silently accepted and dropped —
// this ticket's HARD REQUIREMENT 1. Every other connection is counted
// while "handled"; this ticket has no protocol to run against it yet
// (D/S-06.T3), so it is closed immediately after accept.
func acceptLoop(ln net.Listener, upgrade *UpgradeManager, log *slog.Logger, active *int64, done chan<- struct{}) {
	defer close(done)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if upgrade != nil && upgrade.Draining() {
			_ = conn.Close()
			if log != nil {
				log.Warn("daemon: connection refused during drain", slog.String("error", ErrDraining.Error()))
			}
			continue
		}
		atomic.AddInt64(active, 1)
		_ = conn.Close()
		atomic.AddInt64(active, -1)
	}
}

// drain logs the connection count at drain entry and exit, per this
// ticket's contract ("Active connection count is tracked and logged via
// slog at drain entry and exit"). At this ticket the accept loop closes
// every connection immediately, so there is nothing to wait out beyond
// ln.Close() itself (already done by the caller); the grace window exists
// for D/S-06.T3's real protocol handling to consume once it lands.
func drain(opts RunOptions, active *int64) {
	if opts.Logger == nil {
		return
	}
	opts.Logger.Info("daemon: drain start", slog.Int64("connections", atomic.LoadInt64(active)),
		slog.Duration("shutdown_grace", opts.Settings.ShutdownGrace))
	opts.Logger.Info("daemon: drain end", slog.Int64("connections", atomic.LoadInt64(active)))
}
