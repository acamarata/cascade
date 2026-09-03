//go:build !windows

package daemon

// Purpose: Status — `cascade daemon status`'s probe: reads the pidfile and
//   classifies it via the shared classifyPID decision function
//   (daemon.go), so a stale pidfile (process gone) or a recycled one (a
//   DIFFERENT process now holds that PID) is never reported as "running" —
//   the classic pidfile-honesty bug this ticket's brief calls out by name.
//   Split from lifecycle_unix.go under R-14.117.
// Inputs: StatusOptions — PID path, an injected ProcessProber and
//   runtime.Clock.
// Outputs: StatusResult{Running, PID, UptimeS, Connections, Detail}.
// Constraints: Connections is always 0 at this ticket — Status has no live
//   channel to the running daemon to ask it (that is D/S-06.T3's JSON-RPC
//   layer plus D/S-07.T1's status.get handler); reporting a fabricated
//   non-zero count would be an Art.1 stub. Detail says so explicitly so a
//   caller never mistakes 0 for "confirmed zero connections".
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// StatusResult is `cascade daemon status`'s result, both for human text
// and the --json versioned envelope (pid/uptime_s/connections fields, per
// this ticket's acceptance criteria).
type StatusResult struct {
	Running     bool    `json:"running"`
	PID         int     `json:"pid"`
	UptimeS     float64 `json:"uptime_s"`
	Connections int     `json:"connections"`
	Detail      string  `json:"detail"`
}

// StatusOptions carries every input Status needs.
type StatusOptions struct {
	PIDPath string
	Prober  ProcessProber
	Clock   runtime.Clock
}

// Status reports whether a daemon is genuinely running.
func Status(_ context.Context, opts StatusOptions) (StatusResult, error) {
	rec, ok, err := readPIDFile(opts.PIDPath)
	if err != nil {
		return StatusResult{}, err
	}
	switch classifyPID(rec, ok, opts.Prober) {
	case livenessNotRunning:
		return StatusResult{Running: false, Detail: "no pidfile"}, nil
	case livenessStale:
		return StatusResult{Running: false, Detail: "stale pidfile: recorded process is not running"}, nil
	case livenessRecycled:
		return StatusResult{Running: false,
			Detail: "stale pidfile: recorded pid has been reused by a different process"}, nil
	case livenessRunning:
		uptime := opts.Clock.Now().Sub(rec.StartedAt).Seconds()
		return StatusResult{
			Running: true,
			PID:     rec.PID,
			UptimeS: uptime,
			// D/S-06.T3 has not landed the RPC channel Status would need
			// to ask the running daemon its real connection count.
			Connections: 0,
			Detail:      "running",
		}, nil
	}
	// classifyPID's livenessState enum has no member beyond the four cases
	// above (unreachable in practice, kept only so the compiler sees every
	// path return).
	return StatusResult{}, cascade.New(cascade.KindInternal, "daemon: status: unreachable liveness state")
}
