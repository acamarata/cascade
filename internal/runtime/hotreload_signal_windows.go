//go:build windows

package runtime

// Purpose: the Windows counterpart of hotreload_signal.go. SIGHUP does
//
//	not exist as a deliverable OS signal on Windows (the syscall package
//	does not define it there), so this build carries NO signal-handler
//	registration at all — the absence is structural (a different file,
//	selected by build tag), not a runtime GOOS branch inside one shared
//	file (Art.5 platform parity: "GOOS=windows: SIGHUP handler is
//	absent, build tag asserted" — hotreload_test.go's
//	TestRegisterSIGHUP_WindowsFileHasNoSignalHandling greps this file's
//	source for exactly that property).
//
// Inputs: a *HotReloader (accepted only to keep the call site
//
//	platform-independent; unused here).
//
// Outputs: a no-op stop func.
// Constraints: this file must never import "os/signal" or "syscall" —
//
//	doing so would defeat the "structurally absent" property the platform
//	parity test checks for.
//
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

// RegisterSIGHUP is a no-op on Windows: there is no SIGHUP to register a
// handler for. Daemon-side hot-reload on Windows is driven by the
// fsnotify watcher only (hotreload_watch.go, which is portable); the
// `cascade config reload` CLI verb's SIGHUP-send path returns an explicit
// tier-2 refusal on Windows instead of silently doing nothing (see
// cmd/cascade/config).
func RegisterSIGHUP(_ *HotReloader) func() {
	return func() {}
}
