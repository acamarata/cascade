//go:build !windows

package runtime

// Purpose: the SIGHUP handler that triggers HotReloader.Reload on
//
//	non-Windows builds (08 §3: "daemon fsnotify-watches config.toml
//	(500ms debounce) + SIGHUP"). Build-tagged out entirely on Windows —
//	see hotreload_signal_windows.go — rather than guarded by a runtime
//	GOOS check, so the symbol itself differs per platform and Art.5's
//	platform-parity test can assert on source shape, not just behavior.
//
// Inputs: a *HotReloader to call Reload on.
// Outputs: a stop func that unregisters the signal handler.
// Constraints: no bare time.Now (this file has no clock use at all).
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// RegisterSIGHUP installs a SIGHUP handler that calls hr.Reload on every
// signal, until the returned stop func is called.
func RegisterSIGHUP(hr *HotReloader) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	stopped := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			select {
			case <-ch:
				hr.Reload(context.Background())
			case <-stopped:
				signal.Stop(ch)
				return
			}
		}
	}()

	return func() {
		close(stopped)
		<-done
	}
}
