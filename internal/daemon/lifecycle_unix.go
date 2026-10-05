//go:build !windows

package daemon

// Purpose: Run, `cascade daemon run`'s foreground implementation on every
//   non-Windows platform (06-FORGE-SPEC §2: unix socket, 0600 perms).
//   Prepares the IPC socket, writes the pidfile, publishes the socket
//   (owner-only, through runtime.PrepareOwnerSocket), and serves real JSON-RPC
//   and SSE connections through the http.Server the composition root
//   builds: accepted connections are handed to that server instead of
//   being closed immediately, restoring the reachability internal/rpc's
//   handler and SSE bridge always had in tests but never had in a running
//   daemon. Tracks an active-connection count and drains on SIGTERM/SIGINT
//   within the configured [daemon] shutdown_grace window.
// Inputs: RunOptions: resolved Settings, the pidfile path, an injected
//   *slog.Logger/runtime.Clock, the real *http.Server (Server) built by
//   NewRPCServer, and two test seams: Signals (an injectable os.Signal
//   channel; production wires signal.Notify itself when nil) and Ready
//   (closed once the socket is listening and the pidfile is written,
//   letting a test synchronize on readiness instead of sleeping).
// Outputs: nil on a clean drained shutdown; a typed error on socket/pidfile
//   failure (also recorded in the Manifest as a fail-loud ERROR line).
// Constraints: this file creates and removes the socket and drives the
//   real IPC hand-off (lifecycle_unix_serve.go carries the http.Server
//   wiring itself, split out to stay under the 300-line file cap). It
//   does not construct rpc.Registry, the events bus, or any RPC method:
//   those stay the composition root's job (cmd/cascade/daemon_unix.go)
//   and internal/rpc's own job, never this package's.
// SPORT: internal/daemon (CHANGE).

import (
	"context"
	"log/slog"
	"net"
	"net/http"
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
	// Server is the real IPC HTTP server (built by NewRPCServer, POST
	// /rpc and, when its Handler was constructed with an SSE handler,
	// GET /events too) that every accepted connection is served through.
	// A nil Server falls back to a bare http.Server{Handler:
	// http.NotFoundHandler()} internally, which still accepts and closes
	// connections correctly (accept mechanics and drain refusal both
	// still run for real) but answers nothing on the wire. Production
	// (cmd/cascade/daemon_unix.go) always supplies a real one; tests that
	// only care about accept/drain mechanics may leave this nil.
	Server *http.Server
	// Connections, if non-nil, is the externally-owned active-connection
	// counter Run increments/decrements via http.Server.ConnState
	// (lifecycle_unix_serve.go's serveRPC) instead of allocating its own
	// Run-private counter. The composition root (cmd/cascade/
	// daemon_unix_run.go) passes the SAME pointer to
	// internal/daemon.NewStatusProvider, so status.get's Connections field
	// reports this real, live count rather than a second, disconnected
	// one. nil preserves Run's original behavior (a fresh, Run-private
	// counter); every caller that does not set this field is unaffected.
	Connections *int64
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

	active := opts.Connections
	if active == nil {
		active = new(int64)
	}
	srv := opts.Server
	if srv == nil {
		srv = &http.Server{Handler: http.NotFoundHandler()}
	}
	wrapped := &drainRefusingListener{Listener: ln, upgrade: opts.Upgrade, log: opts.Logger}
	serveDone := serveRPC(wrapped, srv, active)

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

	shutdownRPCServer(srv, opts.Settings.ShutdownGrace)
	_ = ln.Close()
	<-serveDone
	drain(opts, active)
	return nil
}

// setUpStageHook is a test seam called with "pidfile" just before the
// pidfile write, while the prepared socket holds its lock unpublished.
var setUpStageHook = func(string) {}

// setUpSocketAndPIDFile prepares the socket, writes the pidfile, THEN
// publishes the socket, recording any failure on manifest. cleanup removes
// the pidfile and closes the listener (which removes the socket only if it
// is still ours) and must run via defer regardless of how Run later exits.
//
// The order is load-bearing (R-14.205, P1-BF-R130). Start's caller-side
// readiness probe treats "the socket answers a dial" as its only signal
// that the daemon is up, and the next start, status or stop then reads the
// pidfile. So a dialable path must imply the pidfile exists: the write
// completes before the link(2) that makes the path dialable, and two
// statements in one goroutine give every observer that guarantee. With the
// pidfile written after the socket answers, a second `daemon start` could
// read "nothing recorded" and spawn a second daemon; measured against the
// linux container that was a contributing cause of R-14.205's failure.
// Prepare comes first because it takes the socket's lifetime lock: a
// losing concurrent start fails there (KindConflict) and never writes or
// removes the winner's pidfile. A failure after prepare removes the pidfile
// it wrote while still holding the lock, so it can only be its own.
func setUpSocketAndPIDFile(opts RunOptions, manifest *Manifest) (net.Listener, func(), error) {
	if err := os.MkdirAll(filepath.Dir(opts.PIDPath), 0o700); err != nil {
		manifest.Failed(ipcSocketSubsystem, "pidfile dir: "+err.Error())
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: create pidfile directory")
	}
	// The helper's kinds (permission-denied, conflict, unavailable) reach
	// the caller unchanged, as serveSocketReal returns them.
	prep, err := runtime.PrepareOwnerSocket(opts.Settings.SocketPath)
	if err != nil {
		manifest.Failed(ipcSocketSubsystem, err.Error())
		return nil, nil, err
	}
	setUpStageHook("pidfile")
	if err := writePIDFile(opts.PIDPath, pidRecord{PID: os.Getpid(), StartedAt: selfStartTime(opts.Clock)}); err != nil {
		manifest.Failed(ipcSocketSubsystem, "pidfile: "+err.Error())
		prep.Abort()
		return nil, nil, err
	}
	ln, err := prep.Publish()
	if err != nil {
		manifest.Failed(ipcSocketSubsystem, err.Error())
		_ = removePIDFile(opts.PIDPath)
		prep.Abort()
		return nil, nil, err
	}
	// Close alone: it removes the socket path only while that path still
	// names the inode this run bound, then releases the socket lock. An
	// unconditional remove here, run after drain, would delete a
	// successor's socket or another user's entry in a sticky directory.
	socketCleanup := func() { _ = ln.Close() }
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

// drain logs the connection count at drain entry and exit, per this
// ticket's contract ("Active connection count is tracked and logged via
// slog at drain entry and exit"). The caller (Run) has already run the
// real graceful-then-forced http.Server shutdown before calling drain, so
// the count logged at "drain end" reflects the true post-shutdown state,
// not a placeholder.
func drain(opts RunOptions, active *int64) {
	if opts.Logger == nil {
		return
	}
	opts.Logger.Info("daemon: drain start", slog.Int64("connections", atomic.LoadInt64(active)),
		slog.Duration("shutdown_grace", opts.Settings.ShutdownGrace))
	opts.Logger.Info("daemon: drain end", slog.Int64("connections", atomic.LoadInt64(active)))
}
