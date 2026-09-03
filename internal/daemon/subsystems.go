package daemon

// Purpose: the declared-subsystem manifest (R-14.87 fail-loud subsystems):
//   daemon bootstrap registers every subsystem it EXPECTS to start before
//   attempting any of them, so "this subsystem's log line never appeared"
//   is always distinguishable from "this subsystem was never declared" —
//   absence of a log line is never the only evidence of a subsystem's
//   state. At W1 the only subsystem Run registers is the IPC socket
//   listener; later tickets (event bus, scheduler, SSE bridge, MCP socket
//   transport, probes) call Register/Started/Failed against this same
//   Manifest as they land.
// Inputs: a *slog.Logger and runtime.Clock, injected once at construction
//   (Art.7.1 — no bare time.Now, R-14.11).
// Outputs: a structured INFO line on every subsystem's success and a
//   distinct ERROR-level line on disabled/skipped/failed, plus Snapshot(),
//   the accessor D/S-07.T1's status.get handler and the doctor
//   subsystem_census check read.
// Constraints: Art.10.3 — split from daemon.go/lifecycle_unix.go purely to
//   keep each file under the 300-line cap; same package, no import cycle
//   risk. Concurrency-safe: Run's accept loop and its signal-handling
//   goroutine may both touch the manifest.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"sync"

	"github.com/acamarata/cascade/internal/runtime"
)

// SubsystemState is one subsystem's lifecycle state as tracked by Manifest.
type SubsystemState int

const (
	// SubsystemDeclared reports that Register was called; the subsystem has
	// not yet attempted to start.
	SubsystemDeclared SubsystemState = iota
	// SubsystemRunning reports that the subsystem started successfully.
	SubsystemRunning
	// SubsystemError reports that the subsystem failed to bind/start.
	SubsystemError
	// SubsystemDisabled reports that the subsystem is intentionally not
	// started (e.g. config-gated off).
	SubsystemDisabled
	// SubsystemSkipped reports that the subsystem was bypassed for a reason
	// other than explicit disablement (e.g. a platform refusal).
	SubsystemSkipped
)

// String renders the state for structured log fields and Snapshot JSON.
func (s SubsystemState) String() string {
	switch s {
	case SubsystemDeclared:
		return "declared"
	case SubsystemRunning:
		return "running"
	case SubsystemError:
		return "error"
	case SubsystemDisabled:
		return "disabled"
	case SubsystemSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// SubsystemStatus is one Manifest entry's exported snapshot row.
type SubsystemStatus struct {
	Name      string         `json:"name"`
	State     SubsystemState `json:"state"`
	Detail    string         `json:"detail"`
	UpdatedAt string         `json:"updated_at"`
}

// Manifest is the fail-loud declared-subsystem registry. The zero value is
// not usable; construct with NewManifest.
type Manifest struct {
	mu      sync.Mutex
	log     *slog.Logger
	clock   runtime.Clock
	entries map[string]*SubsystemStatus
	order   []string
}

// NewManifest builds a Manifest. A nil logger discards log lines (tests
// that only care about Snapshot state may pass nil); a nil clock falls back
// to runtime.NewSystemClock() — production callers should always inject
// the real one explicitly so the fallback is visible at the call site.
func NewManifest(log *slog.Logger, clock runtime.Clock) *Manifest {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &Manifest{log: log, clock: clock, entries: map[string]*SubsystemStatus{}}
}

// Register declares name as an expected subsystem, in state Declared,
// before any attempt to start it. Calling Register twice for the same name
// is a no-op against the existing entry (idempotent declaration).
func (m *Manifest) Register(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; exists {
		return
	}
	m.entries[name] = &SubsystemStatus{Name: name, State: SubsystemDeclared, UpdatedAt: m.now()}
	m.order = append(m.order, name)
}

// Started records name's successful start at addrOrState (e.g. a socket
// path, a listen address) and logs a structured INFO start line.
func (m *Manifest) Started(name, addrOrState string) {
	m.set(name, SubsystemRunning, addrOrState)
	m.logf(slog.LevelInfo, name, "subsystem started", addrOrState)
}

// Failed records name's bind/start failure and logs a distinct ERROR-level
// line — the fail-loud half of R-14.87.
func (m *Manifest) Failed(name, reason string) {
	m.set(name, SubsystemError, reason)
	m.logf(slog.LevelError, name, "subsystem failed to start", reason)
}

// Disabled records that name was intentionally not started and logs an
// ERROR-level line (R-14.87: disabled is one of the three states that must
// never be silent).
func (m *Manifest) Disabled(name, reason string) {
	m.set(name, SubsystemDisabled, reason)
	m.logf(slog.LevelError, name, "subsystem disabled", reason)
}

// Skipped records that name was bypassed (e.g. a platform refusal) and
// logs an ERROR-level line, same rationale as Disabled.
func (m *Manifest) Skipped(name, reason string) {
	m.set(name, SubsystemSkipped, reason)
	m.logf(slog.LevelError, name, "subsystem skipped", reason)
}

// Snapshot returns every registered subsystem's current status, in
// registration order — the accessor D/S-07.T1's status.get handler and the
// doctor subsystem_census check use.
func (m *Manifest) Snapshot() []SubsystemStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SubsystemStatus, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, *m.entries[name])
	}
	return out
}

func (m *Manifest) set(name string, state SubsystemState, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, exists := m.entries[name]
	if !exists {
		e = &SubsystemStatus{Name: name}
		m.entries[name] = e
		m.order = append(m.order, name)
	}
	e.State = state
	e.Detail = detail
	e.UpdatedAt = m.now()
}

func (m *Manifest) now() string {
	return m.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (m *Manifest) logf(level slog.Level, name, msg, detail string) {
	if m.log == nil {
		return
	}
	m.log.Log(context.Background(), level, msg, slog.String("subsystem", name), slog.String("detail", detail))
}
