// Package daemon implements the cascade daemon's process lifecycle: the
// unix-socket listener, pidfile, graceful SIGTERM/SIGINT drain, the
// fail-loud declared-subsystem manifest (R-14.87), and the
// start/stop/restart/status verbs cmd/cascade/daemon.go wires onto cobra.
//
// Purpose: own everything "is a cascade daemon currently running, and how
//
//	do I make one start/stop/restart" — nothing about what the daemon DOES
//	once it is up (that is D/S-06.T3's JSON-RPC layer, mounted on top of
//	the listener this package creates).
//
// Inputs: resolved daemon settings (socket path, shutdown grace) sourced
//
//	from the C-S04 config loader's *runtime.Config — never hardcoded — plus
//	an injected runtime.PathProvider, runtime.Clock, *slog.Logger, and (in
//	tests) fake ProcessProber/Spawner/Signaler implementations so no test
//	touches a real process, signal, or the real home directory (Art.7.1).
//
// Outputs: Run/Start/Stop/Restart/Status results the CLI layer renders
//
//	through internal/output; a fail-loud subsystem Manifest snapshot later
//	tickets (status.get, doctor subsystem_census) read.
//
// Constraints: platform-split per R-14.7 — lifecycle_unix.go carries the
//
//	real unix implementation, lifecycle_windows.go the tier-2 refusal
//	(06-FORGE-SPEC §2: Windows has no daemon at all). This file holds only
//	the platform-independent pieces: settings resolution, the pidfile wire
//	format, and the liveness classification a stale-vs-recycled pidfile
//	check needs on either platform. Never writes to stdout/stderr directly
//	(internal/build's output gate scans this package too) — all output is
//	returned as data for cmd/cascade/daemon.go to render.
//
// SPORT: internal/daemon (ADD, per T-2 sport_updates).
package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// pidFileName and socket-relative defaults live under PathProvider.Root(),
// matching daemon.sock's own placement in paths.go — this ticket's
// files_scope does not include paths.go, so the pidfile path is derived
// here rather than adding a PidPath() method there.
const pidFileName = "daemon.pid"

// PIDFilePath returns the resolved pidfile path under paths.Root().
func PIDFilePath(paths runtime.PathProvider) string {
	return filepath.Join(paths.Root(), pidFileName)
}

// Settings is the resolved [daemon] section this ticket reads (08 §3):
// socket and shutdown_grace, sourced from *runtime.Config.Extra["daemon"]
// with the socket falling back to PathProvider.SocketPath() (the CASCADE_
// SOCKET / derived-default path C/S-04.T1 already resolves) when the file
// omits it. shutdown_grace carries NO asserted default (the ticket
// contract is explicit: "this ticket asserts no default value — the
// loader's resolved value is used as-is") — GraceSet reports whether the
// key was present at all, and callers that care about "was this configured
// or just absent" read it rather than assuming ShutdownGrace's zero value
// means "configured to zero".
type Settings struct {
	SocketPath    string
	ShutdownGrace time.Duration
	GraceSet      bool
}

// ResolveSettings reads the [daemon] section out of cfg.Extra (it is not a
// typed Config field — only runtime/elevation/logging are typed sections as
// of C/S-04.T1; everything else round-trips through Extra, per
// config_write.go's knownConfigKeys doc) and resolves Settings against
// paths' derived socket default. A malformed shutdown_grace value (present
// but neither a Go duration string nor a plain number of seconds) is a
// typed KindInvalidInput error — this ticket does not silently ignore bad
// config.
func ResolveSettings(cfg *runtime.Config, paths runtime.PathProvider) (Settings, error) {
	s := Settings{SocketPath: paths.SocketPath()}
	if cfg == nil || cfg.Extra == nil {
		return s, nil
	}
	section, ok := cfg.Extra["daemon"].(map[string]interface{})
	if !ok {
		return s, nil
	}
	if sock, ok := section["socket"].(string); ok && sock != "" {
		s.SocketPath = sock
	}
	raw, present := section["shutdown_grace"]
	if !present {
		return s, nil
	}
	grace, err := parseGraceValue(raw)
	if err != nil {
		return Settings{}, cascade.Wrapf(cascade.KindInvalidInput, err,
			"daemon.shutdown_grace: %v", raw)
	}
	s.ShutdownGrace = grace
	s.GraceSet = true
	return s, nil
}

// parseGraceValue accepts either a Go duration string ("5s", per every
// other duration-shaped key this repo's TOML already uses, e.g.
// secrets.clipboard_ttl) or a bare TOML number, interpreted as whole
// seconds (pelletier/go-toml/v2 decodes a TOML integer as int64 and a
// float as float64).
func parseGraceValue(raw interface{}) (time.Duration, error) {
	switch v := raw.(type) {
	case string:
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("not a valid duration: %w", err)
		}
		return d, nil
	case int64:
		return time.Duration(v) * time.Second, nil
	case float64:
		return time.Duration(v * float64(time.Second)), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", raw)
	}
}

// pidRecord is the pidfile's on-disk JSON shape: the daemon's PID and its
// process start time, the pair Status uses to tell a live daemon apart from
// a stale pidfile (process gone) or a recycled one (a DIFFERENT process now
// holds that PID) — a bare PID alone cannot distinguish the last two.
type pidRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// writePIDFile atomically-enough (single os.WriteFile call) writes rec to
// path with 0600 permissions — matching the socket's own 0600 requirement,
// since the pidfile also names a live PID an unprivileged local attacker
// could otherwise probe.
func writePIDFile(path string, rec pidRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "encode pidfile")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "write pidfile")
	}
	return nil
}

// readPIDFile reads and decodes path. A missing file is reported via the
// ok=false, err=nil return (the normal "nothing is running" case, not a
// failure); a present-but-corrupt file is a typed error, since a daemon
// pidfile that fails to parse is a genuine anomaly worth surfacing rather
// than silently treating as "not running".
func readPIDFile(path string) (rec pidRecord, ok bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return pidRecord{}, false, nil
		}
		return pidRecord{}, false, cascade.Wrap(cascade.KindUnavailable, readErr, "read pidfile")
	}
	if unmarshalErr := json.Unmarshal(data, &rec); unmarshalErr != nil {
		return pidRecord{}, false, cascade.Wrap(cascade.KindIntegrity, unmarshalErr, "corrupt pidfile")
	}
	return rec, true, nil
}

// removePIDFile removes path, tolerating its prior absence.
func removePIDFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return cascade.Wrap(cascade.KindUnavailable, err, "remove pidfile")
	}
	return nil
}

// ProcessProber answers the two liveness questions classifyPID needs, kept
// as an injectable interface so no unit test spawns or signals a real OS
// process (Art.7.1). The unix production implementation (lifecycle_unix.go)
// backs IsAlive with a signal-0 probe and StartTime with `ps -o lstart=`;
// tests supply a fake.
type ProcessProber interface {
	// IsAlive reports whether pid currently names a live process.
	IsAlive(pid int) bool
	// StartTime reports the process currently holding pid's start time.
	// ok is false when it cannot be determined (process gone, or the
	// probe itself failed) — callers must not treat a false ok as "start
	// time is the zero value", only as "unknown".
	StartTime(pid int) (time.Time, bool)
}

// livenessState classifies a pidfile record against the live process table.
type livenessState int

const (
	// livenessNotRunning: no pidfile record was supplied at all.
	livenessNotRunning livenessState = iota
	// livenessRunning: pid is alive and its start time matches the
	// pidfile's recorded start time within recycleTolerance.
	livenessRunning
	// livenessStale: pid names no live process (the daemon exited without
	// cleaning up, e.g. it was killed -9).
	livenessStale
	// livenessRecycled: pid IS alive, but its start time does not match —
	// the OS has reused this PID for an unrelated process since the
	// daemon that wrote the pidfile exited. Reporting this as "running"
	// would be the classic pidfile bug this ticket's brief calls out by
	// name.
	livenessRecycled
)

// recycleTolerance absorbs the second-level rounding `ps -o lstart=`
// reports at (lifecycle_unix.go's StartTime implementation) against the
// nanosecond-precision StartedAt this package itself records at write time.
const recycleTolerance = 2 * time.Second

// classifyPID is the platform-independent decision function: given a
// pidfile record and a ProcessProber, decide which of the four liveness
// states applies. Kept pure (no I/O beyond the injected prober) so it is
// exhaustively unit-testable without any real process or pidfile.
func classifyPID(rec pidRecord, ok bool, prober ProcessProber) livenessState {
	if !ok {
		return livenessNotRunning
	}
	if !prober.IsAlive(rec.PID) {
		return livenessStale
	}
	actual, known := prober.StartTime(rec.PID)
	if !known {
		// Best-effort: IsAlive + a matching PID is still stronger signal
		// than nothing, and StartTime is not always resolvable (a probe
		// failure, not proof of recycling). Documented, not silently
		// assumed — see the doc comment above.
		return livenessRunning
	}
	diff := actual.Sub(rec.StartedAt)
	if diff < 0 {
		diff = -diff
	}
	if diff > recycleTolerance {
		return livenessRecycled
	}
	return livenessRunning
}

// NewRPCServer builds the *http.Server that serves the daemon's IPC
// surface on its unix socket: POST rpc.RPCPath ("/rpc") always, and GET
// rpc.EventsPath ("/events") too when sse is non-nil, both routed to the
// SAME rpc.Handler value (rpc.Handler.ServeHTTP already branches on path
// and method internally, per handler.go) so the mux never has to decide
// between NewHandler and NewHandlerWithSSE at more than one call site.
// ConnContext is wired to rpc.ConnContext so every accepted connection's
// socket-peer UID is resolved once and available to the handler's
// ownership check (non-owner UID -> HTTP 403, before the JSON-RPC layer
// is ever reached).
//
// Socket creation and the accept/drain lifecycle live in
// lifecycle_unix.go; Run hands each accepted connection to this server.
// lifecycle_unix.go's accept loop used to close every connection
// immediately instead of ever reaching this server; that gap is closed
// now.
func NewRPCServer(registry *rpc.Registry, sse *rpc.SSEHandler) *http.Server {
	var handler http.Handler
	if sse != nil {
		handler = rpc.NewHandlerWithSSE(registry, sse)
	} else {
		handler = rpc.NewHandler(registry)
	}
	mux := http.NewServeMux()
	mux.Handle(rpc.RPCPath, handler)
	if sse != nil {
		mux.Handle(rpc.EventsPath, handler)
	}
	return &http.Server{
		Handler:     mux,
		ConnContext: rpc.ConnContext,
	}
}
