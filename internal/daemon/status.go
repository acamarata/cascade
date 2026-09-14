package daemon

// Purpose: the status.get JSON-RPC method handler behind `cascade status`
//   (07-CLI-COMMAND-TREE §status: "one-shot summary ✦; rpc: status.get").
//   Every field in StatusResponse is assembled from live daemon state at
//   request time - no placeholder, no field that can only ever hold one
//   value (Art.1). Health and Subsystems are both derived from the real
//   R-14.87 fail-loud subsystem Manifest (subsystems.go), whose own doc
//   comment names this handler as its intended consumer: a subsystem that
//   failed to start (SubsystemError) is visible in Health, not papered
//   over as "ok". A subsystem that is Disabled or Skipped is NOT a
//   failure - Disabled is an operator's own choice and Skipped is a
//   disclosed, expected precondition-absent state (e.g. no symbol graph
//   yet on a fresh install, reachability_wiring.go) - so neither degrades
//   Health, but both stay visible, with their reason, in Subsystems below
//   (DEFECT-status-degraded-on-fresh-install.md: a signal that reads
//   "degraded" on every healthy fresh install conveys no information).
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
	"fmt"
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
	// Health is "ok" when every declared subsystem is running, has not
	// yet been attempted, is deliberately Disabled, or is a disclosed
	// Skipped precondition-absent state; "degraded" only when a subsystem
	// is in SubsystemError - it tried to start and failed. See health()
	// below - this is a real signal, not a constant, and its meaning is
	// "something is genuinely wrong", not "something is merely absent".
	Health string `json:"health"`
	// Subsystems is the live subsystem Manifest's snapshot, in
	// registration order - always populated when a manifest is wired, so
	// a Disabled or Skipped subsystem's name and reason stay visible even
	// though neither affects Health above (subsystem detail is never
	// suppressed, only Health's verdict changed). Omitted from the JSON
	// envelope when empty (nil-manifest test path).
	Subsystems []SubsystemStatus `json:"subsystems,omitempty"`
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
			Health:     p.health(),
			Subsystems: p.subsystems(),
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

// health derives Health from the live subsystem Manifest: "degraded" only
// when a declared subsystem is in SubsystemError - it tried to start and
// failed; "ok" for every other state. SubsystemDisabled (the operator
// chose this) and SubsystemSkipped (a disclosed, expected precondition-
// absent state - e.g. WireReachability's "no stored symbol graph yet" on
// every fresh install, reachability_wiring.go) are deliberately NOT
// degraded: a health signal that reads "degraded" on every healthy
// install conveys no information (DEFECT-status-degraded-on-fresh-
// install.md). Both states stay visible, with their reason, in
// StatusResponse.Subsystems - this function decides only what Health
// MEANS, never what is reported.
func (p *StatusProvider) health() string {
	if p.manifest == nil {
		return "ok"
	}
	for _, s := range p.manifest.Snapshot() {
		switch s.State {
		case SubsystemError:
			return "degraded"
		case SubsystemDeclared, SubsystemRunning, SubsystemDisabled, SubsystemSkipped:
			// Not yet attempted, running cleanly, deliberately disabled,
			// or a disclosed skip - none of these degrade Health; explicit
			// cases keep this switch exhaustive over SubsystemState's full
			// enumeration (golangci-lint's exhaustive rule).
		}
	}
	return "ok"
}

// subsystems returns the live Manifest's snapshot for StatusResponse.
// Subsystems, or nil when no manifest is wired - the same nil-manifest
// fallback health() uses, so a test that omits the manifest gets an empty
// slice rather than a nil-pointer panic.
func (p *StatusProvider) subsystems() []SubsystemStatus {
	if p.manifest == nil {
		return nil
	}
	return p.manifest.Snapshot()
}

// MarshalJSON renders SubsystemState as its String() name (e.g.
// "skipped"), not the underlying int, so status.get's JSON envelope stays
// self-describing - a caller reading Subsystems over the wire sees
// "skipped"/"disabled"/"error", not an opaque small integer.
func (s SubsystemState) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON parses SubsystemState from its String() name - the
// reverse of MarshalJSON above - so a client round-tripping status.get's
// JSON (cmd/cascade's decodeStatusEnvelope, or any future consumer) gets
// back the same state that was on the wire, not a decode error.
func (s *SubsystemState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	switch name {
	case "declared":
		*s = SubsystemDeclared
	case "running":
		*s = SubsystemRunning
	case "error":
		*s = SubsystemError
	case "disabled":
		*s = SubsystemDisabled
	case "skipped":
		*s = SubsystemSkipped
	default:
		return fmt.Errorf("daemon: unknown subsystem state %q", name)
	}
	return nil
}
