//go:build !windows

package daemon

// Purpose: keep stop signals observable throughout an upgrade hand-off.
// Inputs: Run's parent context, signal channel, options and listener.
// Outputs: a cancellable hand-off and a joined signal watcher.
// Constraints: split from lifecycle_unix.go for its 300-line cap; the
// watcher ends before Run resumes reading signals or returns.
// SPORT: internal/daemon (CHANGE).

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// notifySignals subscribes before the socket is dialable: an UpgradeSignal
// must reach the hand-off instead of Go's default action
// (which Go ignores, so the hand-off request would be lost).
func notifySignals(opts RunOptions) (<-chan os.Signal, func()) {
	if opts.Signals != nil {
		return opts.Signals, func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, UpgradeSignal)
	return ch, func() { signal.Stop(ch) }
}

// handleUpgradeWithSignals cancels the hand-off on a stop, and records the
// stop even when no drain occurred, so awaitStop takes normal shutdown.
func handleUpgradeWithSignals(ctx context.Context, opts RunOptions, ln net.Listener, sigs <-chan os.Signal) (bool, bool) {
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		for {
			select {
			case <-finished:
				return
			case <-hctx.Done():
				return
			case sig, ok := <-sigs:
				if !ok || sig != UpgradeSignal { // same stop rule as awaitStop
					cancel()
					return
				}
			}
		}
	}()
	relaunched, drained := handleUpgradeSignal(hctx, opts, ln)
	close(finished)
	<-joined
	if hctx.Err() != nil {
		return false, true
	}
	return relaunched, drained
}
