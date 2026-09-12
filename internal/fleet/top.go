// Package fleet (top.go): `cascade fleet top`'s interactive bubbletea Model
//
//	(P1-E18-W4-S40-T1) plus the SSE replay fold that drives it from
//	either a live daemon stream or the recorded fixture
//	(internal/fleet/testdata/top-sse-fixture.ndjson). The Model's
//	Update/View pair is exercised directly in top_test.go with NO
//	tea.Program, NO TTY and NO real clock: every redraw is driven by
//	feeding a msg value in and reading the returned string back out,
//	exactly the "TESTABLE WITHOUT A TTY" requirement this ticket's
//	prompt states. cmd/cascade/fleet_top.go wires the real
//	tea.NewProgram around this Model for the actual interactive command.
//
// Inputs: an initial TopSnapshot, a runtime.Clock, and a stream of
//
//	tea.Msg values (key presses, window-size changes, decoded snapshot/
//	session-event updates, a disconnect error).
//
// Outputs: Model.View()'s rendered frame (via top_render.go's
//
//	RenderFrame); Model.Update's returned tea.Cmd (tea.Quit on q/
//	Ctrl-C or a fatal disconnect).
//
// Constraints: Model never reads a bare clock or terminal size itself —
//
//	every input arrives as a message top.go's caller constructs, so the
//	whole redraw path is deterministic (Art.7.3).
//
// SPORT: internal.fleet.top/ADDED (P1-E18-W4-S40-T1).
package fleet

import (
	"bufio"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/acamarata/cascade/internal/runtime"
)

// SnapshotMsg carries a freshly fetched or freshly folded TopSnapshot
// into the Model.
type SnapshotMsg struct{ Snapshot TopSnapshot }

// SessionEventMsg carries one decoded session-changed row into the
// Model, which folds it into its current snapshot via ApplySessionEvent.
type SessionEventMsg struct{ Row TopSessionRow }

// DisconnectMsg reports that the daemon connection ended (stream closed
// or errored). Err is never nil.
type DisconnectMsg struct{ Err error }

// Model is `cascade fleet top`'s bubbletea model.
type Model struct {
	snapshot     TopSnapshot
	clock        runtime.Clock
	width        int
	height       int
	quitting     bool
	disconnected error
}

// NewModel builds a Model with an initial snapshot and an injected
// clock, never a bare time source.
func NewModel(initial TopSnapshot, clk runtime.Clock) Model {
	return Model{snapshot: initial, clock: clk, width: 80, height: 24}
}

// Init implements tea.Model. No startup command: the caller (cmd/cascade)
// is responsible for pumping SnapshotMsg/SessionEventMsg values in via
// p.Send from its own fetch/SSE goroutines.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		return m, nil
	case tea.KeyMsg:
		switch v.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case SnapshotMsg:
		m.snapshot = v.Snapshot
		return m, nil
	case SessionEventMsg:
		m.snapshot = ApplySessionEvent(m.snapshot, v.Row, m.clock)
		return m, nil
	case DisconnectMsg:
		m.disconnected = v.Err
		return m, tea.Quit
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.disconnected != nil {
		return "cascade fleet top: daemon disconnected: " + m.disconnected.Error() + "\n"
	}
	return RenderFrame(m.snapshot, m.width, m.height)
}

// Disconnected reports the reason the Program quit due to a
// DisconnectMsg, or nil if it quit normally (q/Ctrl-C) or is still
// running.
func (m Model) Disconnected() error { return m.disconnected }

// FoldSSE reads r's WHATWG SSE stream line by line (the same "data:"
// grammar cmd/cascade/fleet_watch.go's watchFleetSessionsLoop parses),
// decoding each block via DecodeSessionEvent and folding it into initial
// via ApplySessionEvent, in order. Malformed blocks are skipped, never
// fatal — mirroring decodeSessionEvent's own ok-bool contract. onRow, if
// non-nil, is invoked with each decoded row as it arrives — this is the
// real production caller (cmd/cascade/fleet_top.go's pumpFleetTopSSE
// passes p.Send as onRow, forwarding SessionEventMsg to the live
// Program), and internal/fleet/top_test.go's replay test passes nil,
// consuming only the final folded TopSnapshot.
func FoldSSE(r io.Reader, initial TopSnapshot, clk runtime.Clock, onRow func(TopSessionRow)) TopSnapshot {
	snap := initial
	scanner := bufio.NewScanner(r)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		if line != "" || len(data) == 0 {
			continue
		}
		block := strings.Join(data, "\n")
		data = nil
		row, ok := DecodeSessionEvent([]byte(block), clk)
		if !ok {
			continue
		}
		snap = ApplySessionEvent(snap, row, clk)
		if onRow != nil {
			onRow(row)
		}
	}
	return snap
}
