// Package fleet (top_fetch.go): `cascade fleet top`'s data-fetching seam
//
//	(P1-E18-W4-S40-T1) —
//
//	SessionLister/GovernorReader/TaskReader interfaces a Fetcher composes
//	into one TopSnapshot, plus ApplySessionEvent, the SSE-driven
//	incremental-update path top.go's Model feeds each decoded session
//	event through. No IO of its own: production adapters (dialing the
//	daemon, sampling the host) live in cmd/cascade/fleet_top.go, so this
//	file is fully unit-testable with fakes (top_fetch_test.go) and the
//	recorded SSE fixture (top_test.go).
//
// Inputs: a SessionLister, an optional GovernorReader, an optional
//
//	TaskReader, and a runtime.Clock (never a bare time.Now — Art.7.3).
//
// Outputs: a TopSnapshot per Fetch call; an updated TopSnapshot per
//
//	ApplySessionEvent call.
//
// Constraints: Fetch never partially populates a snapshot on error — a
//
//	failed session list aborts the whole fetch (fail-closed), matching
//	fetchFleetSessions's own error-propagation shape in cmd/cascade.
//
// SPORT: internal.fleet.top/ADDED (P1-E18-W4-S40-T1).
package fleet

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/runtime"
)

// SessionLister lists the current fleet session rows. The production
// implementation (cmd/cascade/fleet_top.go) wraps sessions.NewClient's
// List call over the daemon socket, mirroring fetchFleetSessionsDaemon's
// own row-mapping shape.
type SessionLister interface {
	List(ctx context.Context) ([]TopSessionRow, error)
}

// GovernorReader reads the current best-effort GovernorPanel. Returning a
// GovernorPanel with Available == false is a legitimate, expected result
// (e.g. before the sampler's first tick, or on an unsupported platform),
// never an error.
type GovernorReader interface {
	Snapshot() GovernorPanel
}

// TaskReader reads the current best-effort TaskPanel. Returning
// Available == false is a legitimate result when no cascade-pbd ticket
// context is resolvable.
type TaskReader interface {
	Snapshot() TaskPanel
}

// SamplerGovernorReader adapts a *governor.Sampler (and, when present, a
// *governor.AdmissionController for QueueDepth) into a GovernorReader.
//
// CONTRACT NOTE (disclosed, not papered over): this ticket's files_scope
// does not include internal/fleet/governor or internal/rpc, so there is
// no daemon-side RPC exposing the DAEMON's own governor state to a
// separate `cascade fleet top` process. SamplerGovernorReader therefore
// samples the LOCAL host's resources via a Sampler constructed in the
// CLI process itself (legitimate for the common single-host case, since
// resource pressure is host-wide), not the daemon's own admission
// controller instance — CompileLockHolders and ThrottleTier are left at
// their zero value because AdmissionController exposes no getter for its
// resolved ThrottleStage or its internal CompileLockRegistry's counts
// beyond QueueDepth(), which this reader does populate when an
// AdmissionController is supplied.
type SamplerGovernorReader struct {
	Sampler   *governor.Sampler
	Admission *governor.AdmissionController
}

// Snapshot implements GovernorReader.
func (r SamplerGovernorReader) Snapshot() GovernorPanel {
	if r.Sampler == nil {
		return GovernorPanel{}
	}
	snap := r.Sampler.Snapshot()
	if snap.SampledAt.IsZero() {
		return GovernorPanel{}
	}
	panel := GovernorPanel{
		Available:      true,
		CPUFraction:    snap.CPUFraction,
		MemUsedBytes:   snap.MemUsedBytes,
		MemTotalBytes:  snap.MemTotalBytes,
		SwapUsedBytes:  snap.SwapUsedBytes,
		SwapTotalBytes: snap.SwapTotalBytes,
	}
	if r.Admission != nil {
		panel.QueueDepth = r.Admission.QueueDepth()
	}
	return panel
}

// UnavailableTaskReader is the default TaskReader: no cascade-pbd ticket
// context is wired in this ticket (the full_desc's own "if cascade-pbd
// is installed" condition), so it always reports an honest, explained
// empty state.
type UnavailableTaskReader struct{}

// Snapshot implements TaskReader.
func (UnavailableTaskReader) Snapshot() TaskPanel {
	return TaskPanel{Unavailable: "no cascade-pbd ticket context wired (P1-E18-W4-S40-T1 scope)"}
}

// Fetcher composes a full TopSnapshot from its three data sources plus an
// injected Clock for GeneratedAt.
type Fetcher struct {
	Sessions SessionLister
	Governor GovernorReader
	Task     TaskReader
	Clock    runtime.Clock
}

// Fetch builds one TopSnapshot. A nil Governor/Task reader is treated as
// an always-unavailable source rather than a required dependency, so
// tests may exercise Fetch with only a SessionLister.
func (f *Fetcher) Fetch(ctx context.Context) (TopSnapshot, error) {
	rows, err := f.Sessions.List(ctx)
	if err != nil {
		return TopSnapshot{}, err
	}
	var gov GovernorPanel
	if f.Governor != nil {
		gov = f.Governor.Snapshot()
	}
	var task TaskPanel
	if f.Task != nil {
		task = f.Task.Snapshot()
	}
	clk := f.Clock
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	return NewTopSnapshot(rows, gov, task, clk.Now()), nil
}

// sessionChangedEvent is the wire shape a fleet.sessions.changed SSE
// event decodes into — the same field set cmd/cascade's own
// decodeSessionEvent (fleet_watch.go) reads off Store.emit's payload,
// duplicated here rather than imported because internal/fleet/sessions'
// SessionRecord carries fields (PID, activity columns) this panel does
// not render; a local, narrower wire shape keeps this file's JSON
// decoding independent of that domain type's own evolution.
type sessionChangedEvent struct {
	SessionID string `json:"session_id"`
	Harness   string `json:"harness"`
	Account   string `json:"account"`
	State     string `json:"state"`
	UpdatedAt int64  `json:"updated_at"`
}

// DecodeSessionEvent decodes one SSE "data:" block as a
// fleet.sessions.changed payload, elapsed formatted against clk. It
// never panics on malformed input, mirroring decodeSessionEvent's own
// ok-bool contract in cmd/cascade/fleet_watch.go.
func DecodeSessionEvent(data []byte, clk runtime.Clock) (TopSessionRow, bool) {
	var ev sessionChangedEvent
	if err := json.Unmarshal(data, &ev); err != nil || ev.SessionID == "" {
		return TopSessionRow{}, false
	}
	return TopSessionRow{
		SessionID: ev.SessionID,
		Harness:   ev.Harness,
		State:     ev.State,
		Account:   ev.Account,
		Elapsed:   formatTopElapsed(ev.UpdatedAt, clk),
	}, true
}

// ApplySessionEvent folds one decoded session row into snap, returning
// the updated snapshot with Summary re-derived. snap itself is never
// mutated in place (UpsertSession returns a copy).
func ApplySessionEvent(snap TopSnapshot, row TopSessionRow, clk runtime.Clock) TopSnapshot {
	sessionsOut := UpsertSession(snap.Sessions, row)
	return NewTopSnapshot(sessionsOut, snap.Governor, snap.Task, clk.Now())
}

// formatTopElapsed mirrors cmd/cascade's own formatElapsed (fleet.go),
// duplicated here so internal/fleet does not import cmd/cascade (which
// would invert the import direction the cmd-rpc-server-boundary rule
// establishes).
func formatTopElapsed(updatedAt int64, clk runtime.Clock) string {
	if updatedAt <= 0 {
		return "-"
	}
	d := clk.Now().Unix() - updatedAt
	if d < 0 {
		d = 0
	}
	return (time.Duration(d) * time.Second).String()
}
