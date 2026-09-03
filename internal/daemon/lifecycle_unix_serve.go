//go:build !windows

package daemon

// Purpose: the real IPC hand-off Run uses: a drain-refusing net.Listener
//   wrapper, the http.Server.Serve/ConnState wiring that replaces the old
//   placeholder accept loop, and the graceful-then-forced shutdown
//   sequence Run calls before drain. Split out of lifecycle_unix.go to
//   stay under the 300-line file cap.
// Inputs: the real unix net.Listener Run's setUpSocketAndPIDFile already
//   built, the *http.Server Run resolves (opts.Server or a fallback), and
//   the shared *int64 active-connection counter drain() reads.
// Outputs: a real, serving http.Server over the daemon socket; a done
//   channel that closes once Serve returns; a shutdown sequence that
//   never leaves a connection open past Run's return.
// Constraints: no bare time.Now/After/Tick/Sleep (none needed here:
//   context.WithTimeout is a duration, not a wall-clock read). Every
//   accepted connection is closed exactly once: drainRefusingListener
//   closes a during-drain connection itself and never hands it to Serve;
//   every other connection's close is driven by http.Server itself
//   (graceful during Shutdown, forced by the trailing Close).
// SPORT: internal/daemon (CHANGE).

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// drainRefusingListener wraps ln so a connection accepted while upgrade is
// mid-Drain is refused with ErrDraining (closed, logged, never handed to
// http.Server.Serve) rather than silently accepted and dropped. This is
// the exact refusal behavior the prior acceptLoop implemented directly;
// wrapping the listener is what lets http.Server.Serve own accepting and
// real protocol handling while that behavior survives unchanged, proven
// by this package's drain-refusal tests.
type drainRefusingListener struct {
	net.Listener
	upgrade *UpgradeManager
	log     *slog.Logger
}

// Accept implements net.Listener. It loops past any connection refused for
// draining rather than returning it, so http.Server.Serve (the caller)
// never sees one.
func (l *drainRefusingListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.upgrade != nil && l.upgrade.Draining() {
			_ = conn.Close()
			if l.log != nil {
				l.log.Warn("daemon: connection refused during drain", slog.String("error", ErrDraining.Error()))
			}
			continue
		}
		return conn, nil
	}
}

// serveRPC hands ln to srv.Serve in its own goroutine, wiring ConnState so
// active tracks the true count of currently-open connections (incremented
// on StateNew, decremented on StateClosed/StateHijacked). This is accurate
// across keep-alive HTTP requests and long-lived SSE streams alike, unlike
// a naive per-Accept counter, which the prior placeholder accept loop
// could get away with only because it closed every connection immediately.
// The returned channel closes once Serve returns (ln closed or srv shut
// down).
func serveRPC(ln net.Listener, srv *http.Server, active *int64) <-chan struct{} {
	srv.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			atomic.AddInt64(active, 1)
		case http.StateClosed, http.StateHijacked:
			atomic.AddInt64(active, -1)
		case http.StateActive, http.StateIdle:
			// No count change: these are transitions of an already-New
			// connection between "serving a request" and "waiting for
			// the next one" (keep-alive), not open/close events.
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	return done
}

// shutdownRPCServer gracefully drains srv (closing its listener, waiting
// up to grace for in-flight requests and SSE streams to finish) and then
// unconditionally force-closes whatever remains, so every accepted
// connection is closed exactly once on shutdown, even when grace elapses
// with connections still open. A grace of zero is valid (some tests use it
// deliberately): Shutdown then returns almost
// immediately via its already-expired deadline and Close does the rest.
func shutdownRPCServer(srv *http.Server, grace time.Duration) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = srv.Close()
}
