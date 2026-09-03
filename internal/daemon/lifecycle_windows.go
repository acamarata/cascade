//go:build windows

package daemon

// Purpose: the Windows counterpart of lifecycle_unix*.go. Windows is
//   tier-2 (06-FORGE-SPEC §2: binary + headless one-shot only, no daemon at
//   all — see D/S-07.T4's daemonless embedded one-shot). Every verb
//   refuses with the SAME typed KindUnsupported error whose hint points at
//   that daemonless path, per this ticket's contract; status refuses with
//   its own explicit "daemon not supported on this platform" message.
//   There is no Windows socket, no Windows pidfile signal handling, and no
//   named-pipe transport — those are explicit post-2.0 deferrals, not an
//   oversight (R-14.131: a platform that only COMPILES here must never
//   claim parity it cannot demonstrate).
// Inputs: the same option structs as the unix implementation, so
//   cmd/cascade/daemon.go's call sites never branch on GOOS themselves —
//   the platform difference is structurally which file's symbols got
//   compiled in, not a runtime branch (Art.5, matching
//   hotreload_signal_windows.go's established pattern in this repo).
// Outputs: always a non-nil typed error; the zero-value result struct.
// Constraints: this file must never import anything socket/signal/process-
//   related — the refusal is unconditional, not a runtime GOOS check
//   wrapped around real logic that would silently rot untested.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// windowsRefusalHint is shared by every verb's refusal message.
const windowsRefusalHint = "cascade has no daemon on Windows (tier-2); " +
	"commands run daemonless on Windows via the automatic socket-probe fallback; see D/S-07.T4"

// RunOptions is intentionally empty on Windows: there is nothing to
// configure for a verb that unconditionally refuses. cmd/cascade's
// platform-specific glue (daemon_windows.go) constructs it, never
// cmd/cascade/daemon.go itself — see that file's doc comment for why the
// two platforms do not need field-identical Options structs.
type RunOptions struct{}

// Run always refuses on Windows.
func Run(_ context.Context, _ RunOptions) error {
	return cascade.New(cascade.KindUnsupported, "daemon run: "+windowsRefusalHint)
}

// StartResult mirrors the unix shape (fields simply stay zero here).
type StartResult struct {
	AlreadyRunning bool
	PID            int
}

// StartOptions is empty on Windows; see RunOptions's doc comment.
type StartOptions struct{}

// Start always refuses on Windows.
func Start(_ context.Context, _ StartOptions) (StartResult, error) {
	return StartResult{}, cascade.New(cascade.KindUnsupported, "daemon start: "+windowsRefusalHint)
}

// StopResult mirrors the unix shape (fields simply stay zero here).
type StopResult struct {
	WasRunning bool
	Escalated  bool
}

// StopOptions is empty on Windows; see RunOptions's doc comment.
type StopOptions struct{}

// Stop always refuses on Windows.
func Stop(_ context.Context, _ StopOptions) (StopResult, error) {
	return StopResult{}, cascade.New(cascade.KindUnsupported, "daemon stop: "+windowsRefusalHint)
}

// RestartResult mirrors the unix shape (fields simply stay zero here).
type RestartResult struct {
	StopResult  StopResult
	StartResult StartResult
}

// RestartOptions is empty on Windows; see RunOptions's doc comment.
type RestartOptions struct{}

// Restart always refuses on Windows.
func Restart(_ context.Context, _ RestartOptions) (RestartResult, error) {
	return RestartResult{}, cascade.New(cascade.KindUnsupported, "daemon restart: "+windowsRefusalHint)
}

// StatusResult mirrors the unix shape (fields simply stay zero here).
type StatusResult struct {
	Running     bool    `json:"running"`
	PID         int     `json:"pid"`
	UptimeS     float64 `json:"uptime_s"`
	Connections int     `json:"connections"`
	Detail      string  `json:"detail"`
}

// StatusOptions is empty on Windows; see RunOptions's doc comment.
type StatusOptions struct{}

// Status refuses on Windows with its own explicit message (this ticket's
// contract calls it out separately from the other four verbs' shared
// hint): "daemon not supported on this platform".
func Status(_ context.Context, _ StatusOptions) (StatusResult, error) {
	return StatusResult{}, cascade.New(cascade.KindUnsupported, "daemon status: daemon not supported on this platform")
}
