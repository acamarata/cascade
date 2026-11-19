package background

import (
	"context"
	"os"
	"syscall"
)

// Signal is an OS signal used for poller shutdown coordination.
type Signal = os.Signal

// Common signals used to trigger clean shutdown of a Poller.
var (
	// SigINT represents the interrupt signal.
	SigINT Signal = syscall.SIGINT
	// SigTERM represents the termination signal.
	SigTERM Signal = syscall.SIGTERM
	// SigHUP represents the hangup signal.
	SigHUP Signal = syscall.SIGHUP
)

// isShutdownSignal reports whether sig requests a clean shutdown.
func isShutdownSignal(sig Signal) bool {
	return sig == SigINT || sig == SigTERM || sig == SigHUP
}

// waitForShutdown blocks until ctx is cancelled, a shutdown signal arrives
// on sigs, or a tick fires. It returns true when the caller should stop.
// A nil or closed sigs channel disables signal input to avoid spinning.
// Shutdown signals and context cancellation take priority over a ready tick
// to ensure prompt shutdown even when a tick is already buffered.
func waitForShutdown(ctx context.Context, sigs <-chan Signal, tick <-chan struct{}) bool {
	for {
		if sigs == nil {
			select {
			case <-ctx.Done():
				return true
			case <-tick:
				return false
			}
		}
		// Priority check: drain shutdown signals and context without blocking.
		// The default branch ensures we only proceed to the blocking select
		// when no shutdown condition is already satisfied.
		select {
		case <-ctx.Done():
			return true
		case sig, ok := <-sigs:
			if !ok {
				// Channel closed; disable signal input and loop to nil-sigs path.
				sigs = nil
				continue
			}
			if isShutdownSignal(sig) {
				return true
			}
			// Unknown signal; ignore and check again.
			continue
		default:
		}
		// Block until something happens.
		select {
		case <-ctx.Done():
			return true
		case sig, ok := <-sigs:
			if !ok {
				sigs = nil
				continue
			}
			if isShutdownSignal(sig) {
				return true
			}
			// Unknown signal; ignore and continue.
		case <-tick:
			return false
		}
	}
}
