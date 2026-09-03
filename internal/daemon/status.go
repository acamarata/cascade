package daemon

// Purpose: the status.get JSON-RPC method handler behind `cascade status`
//   (07-CLI-COMMAND-TREE §status: "one-shot summary ✦; rpc: status.get").
//   Every field in StatusResponse is assembled from live daemon state at
//   request time - no placeholder, no field that can only ever hold one
//   value (Art.1). Health specifically is derived from the real
//   R-14.87 fail-loud subsystem Manifest (subsystems.go), whose own doc
//   comment names this handler as its intended consumer, so a subsystem
//   that failed to start is visible in Health, not papered over as "ok".
// Inputs: an injected runtime.Clock and a fixed start time (Art.7.1 - no
//   bare time.Now), the resolved socket path, an externally-owned
//   active-connection counter, and the daemon's real subsystem Manifest.
// Outputs: StatusResponse, JSON-marshaled as status.get's JSON-RPC result.
// Constraints: registered on the daemon's own rpc.Registry by the
//   composition root (cmd/cascade/daemon_unix_run.go's buildRPCServer),
//   the SAME registry the MCP dispatcher registers on, so both are
//   reachable on the daemon's one real unix socket (R-14.166).
// SPORT: internal/daemon (ADD, per T-1 sport_updates).

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// StatusMethod is the status.get JSON-RPC method name.
const StatusMethod = "status.get"

// StatusResponse is status.get's result: a one-shot aggregate snapshot of
// this daemon process.
type StatusResponse struct {
	// Version is the running binary's release version
	// (internal/buildinfo.Version - "dev" for an unstamped build, the real
	// ldflags-stamped tag otherwise; never a hardcoded literal).
	Version string             `json:"version"`
	Daemon  StatusDaemonFields `json:"daemon"`
	// Health is "ok" when every declared subsystem is running (or has not
	// yet been attempted), "degraded" when any declared subsystem is in
	// an error, disabled, or skipped state. See health() below - this is
	// a real signal, not a constant.
	Health string `json:"health"`
}

// StatusDaemonFields is StatusResponse.Daemon's shape.
type StatusDaemonFields struct {
	// PID is this process's own process id (os.Getpid()) - status.get only
	// ever runs inside the daemon process itself, so this is always the
	// daemon's real PID, matching the pidfile Run wrote.
	PID int `json:"pid"`
	// UptimeS is the daemon's uptime in seconds, computed from the
	// injected Clock against the start time recorded at composition-root
	// construction (a few milliseconds before Run's own pidfile write;
	// see NewStatusProvider's doc comment).
	UptimeS float64 `json:"uptime_s"`
	// Connections is the daemon's live accepted-connection count, read
	// from the SAME *int64 counter Run's http.Server.ConnState hook
	// maintains (lifecycle_unix_serve.go's serveRPC) - not a second,
	// disconnected counter. See RunOptions.Connections.
	Connections int `json:"connections"`
	// SocketPath is the resolved unix socket path Run is actually bound
	// to (daemon.Settings.SocketPath).
	SocketPath string `json:"socket_path"`
}

// StatusProvider assembles StatusResponse from live state at request time.
// Constructed once by the composition root and registered as the
// status.get handler; never constructs its own state.
type StatusProvider struct {
	clock       runtime.Clock
	start       time.Time
	socketPath  string
	connections *int64
	manifest    *Manifest
}

// NewStatusProvider builds a StatusProvider.
//
//   - clock/start: uptime is clock.Now().Sub(start), using the SAME
//     injected Clock Run uses. start is read once by the composition root
//     immediately before calling Run, not a placeholder.
//   - socketPath: the resolved daemon.Settings.SocketPath, the exact path
//     Run binds.
//   - connections: an externally-owned *int64. The composition root passes
//     this SAME pointer to RunOptions.Connections so Run's real
//     ConnState-driven counter is what this handler reads. A nil pointer
//     reports 0 without pretending a live count was measured.
//   - manifest: the SAME *Manifest the composition root also passes as
//     RunOptions.Manifest, so Run's own Register/Started/Failed calls
//     against it are visible here. A nil manifest reports Health "ok"
//     unconditionally (only reachable from a test that omits it).
func NewStatusProvider(clock runtime.Clock, start time.Time, socketPath string, connections *int64, manifest *Manifest) *StatusProvider {
	return &StatusProvider{
		clock:       clock,
		start:       start,
		socketPath:  socketPath,
		connections: connections,
		manifest:    manifest,
	}
}

// Handler returns the status.get rpc.HandlerFunc, ready for
// Registry.Register(StatusMethod, ...).
func (p *StatusProvider) Handler() rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, err, "status.get: context canceled")
		}
		return StatusResponse{
			Version: buildinfo.Version,
			Daemon: StatusDaemonFields{
				PID:         os.Getpid(),
				UptimeS:     p.clock.Now().Sub(p.start).Seconds(),
				Connections: p.liveConnections(),
				SocketPath:  p.socketPath,
			},
			Health: p.health(),
		}, nil
	}
}

// liveConnections reads the shared active-connection counter, or 0 when
// none was wired in.
func (p *StatusProvider) liveConnections() int {
	if p.connections == nil {
		return 0
	}
	return int(atomic.LoadInt64(p.connections))
}

// health derives Health from the live subsystem Manifest: "degraded" when
// any declared subsystem is in SubsystemError, SubsystemDisabled, or
// SubsystemSkipped state; "ok" otherwise (including SubsystemRunning and
// the transient SubsystemDeclared state a subsystem passes through before
// its first Started/Failed/Disabled/Skipped call).
func (p *StatusProvider) health() string {
	if p.manifest == nil {
		return "ok"
	}
	for _, s := range p.manifest.Snapshot() {
		switch s.State {
		case SubsystemError, SubsystemDisabled, SubsystemSkipped:
			return "degraded"
		case SubsystemDeclared, SubsystemRunning:
			// Not yet attempted, or running cleanly - neither degrades
			// Health; explicit cases keep this switch exhaustive over
			// SubsystemState's full enumeration (golangci-lint's
			// exhaustive rule).
		}
	}
	return "ok"
}
