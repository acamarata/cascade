// Package fleet (top_render.go): `cascade fleet top`'s pure rendering
//
//	functions (P1-E18-W4-
//
//	S40-T1, R-21.268's pinned bubbletea+lipgloss+bubbles stack) — turning
//	a TopSnapshot into either the interactive frame text (RenderFrame,
//	built on lipgloss for panel styling and bubbles/table for the
//	sessions grid) or the `--once --json` payload (RenderSnapshotJSON).
//	Every function here takes a TopSnapshot and returns a string/[]byte;
//	none does IO, so top_render_test.go exercises them directly without
//	a TTY, a real terminal size, or a running program.
//
// Inputs: a TopSnapshot, a terminal width/height pair.
// Outputs: a plain string (RenderFrame) or JSON bytes (RenderSnapshotJSON).
// Constraints: RenderFrame forces an ASCII (no-color) lipgloss profile so
//
//	its output is deterministic across CI terminals/NO_COLOR settings —
//	the interactive Program (top.go) applies real terminal styling itself
//	via its own renderer, this function is the content, not the color.
//
// SPORT: internal.fleet.top/ADDED (P1-E18-W4-S40-T1).
package fleet

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// topRenderer is a lipgloss Renderer pinned to termenv.Ascii (no color
// codes at all) and written to io.Discard - it exists only to derive
// styles, never to write - so RenderFrame's output is byte-identical
// regardless of the calling process's terminal, NO_COLOR setting, or CI
// environment (Art.7.3 determinism). The interactive Program (top.go)
// builds its own renderer against the real terminal separately.
var topRenderer = lipgloss.NewRenderer(io.Discard, termenv.WithProfile(termenv.Ascii))

// topPanelStyle is the shared box style every panel renders inside.
var topPanelStyle = topRenderer.NewStyle().
	Border(lipgloss.NormalBorder()).
	Padding(0, 1)

// RenderFrame renders all four panels into one plain-text frame sized to
// width. height is accepted for the interactive layout's future use
// (e.g. truncating a long sessions table) and is not yet load-bearing in
// this render — an explicit, disclosed no-op rather than silently
// ignored.
func RenderFrame(snap TopSnapshot, width, height int) string {
	_ = height
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(topPanelStyle.Width(width - 2).Render(renderSessionsTable(snap.Sessions)))
	b.WriteByte('\n')
	b.WriteString(topPanelStyle.Width(width - 2).Render(renderGovernorPanel(snap.Governor)))
	b.WriteByte('\n')
	b.WriteString(topPanelStyle.Width(width - 2).Render(renderTaskPanel(snap.Task)))
	b.WriteByte('\n')
	b.WriteString(renderSummaryLine(snap.Summary))
	return b.String()
}

// renderSessionsTable builds the sessions panel via bubbles/table,
// rendering an empty-but-headered table when there are no sessions
// (an explicit empty state, not an error).
func renderSessionsTable(rows []TopSessionRow) string {
	cols := []table.Column{
		{Title: "SESSION", Width: 16},
		{Title: "HARNESS", Width: 10},
		{Title: "STATE", Width: 10},
		{Title: "LANE", Width: 10},
		{Title: "ACCOUNT", Width: 10},
		{Title: "ELAPSED", Width: 10},
	}
	trows := make([]table.Row, 0, len(rows))
	for _, r := range rows {
		trows = append(trows, table.Row{r.SessionID, r.Harness, r.State, r.Lane, r.Account, r.Elapsed})
	}
	// Styles are pinned to topRenderer (termenv.Ascii) rather than
	// table.DefaultStyles' own global-renderer-derived colors, so this
	// output stays byte-identical regardless of the calling process's
	// terminal or NO_COLOR state (Art.7.3 determinism).
	styles := table.Styles{
		Header:   topRenderer.NewStyle().Bold(true),
		Cell:     topRenderer.NewStyle(),
		Selected: topRenderer.NewStyle(),
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(trows),
		table.WithHeight(len(trows)+1),
		table.WithFocused(false),
		table.WithStyles(styles),
	)
	return "Sessions\n" + t.View()
}

// renderGovernorPanel renders the resource/admission panel, showing an
// explicit "unavailable" line rather than misleading zero values when
// panel.Available is false.
func renderGovernorPanel(panel GovernorPanel) string {
	if !panel.Available {
		return "Governor\nunavailable: no resource sample yet"
	}
	return fmt.Sprintf(
		"Governor\ncpu=%.0f%% mem=%d/%dMB swap=%d/%dMB queue=%d",
		panel.CPUFraction*100,
		panel.MemUsedBytes/(1<<20), panel.MemTotalBytes/(1<<20),
		panel.SwapUsedBytes/(1<<20), panel.SwapTotalBytes/(1<<20),
		panel.QueueDepth,
	)
}

// renderTaskPanel renders the active-task panel, disclosing why it is
// empty when Available is false rather than showing a blank box.
func renderTaskPanel(panel TaskPanel) string {
	if !panel.Available {
		reason := panel.Unavailable
		if reason == "" {
			reason = "no active ticket"
		}
		return "Task\nunavailable: " + reason
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task\nticket=%s", panel.TicketID)
	if panel.LastEvent != "" {
		fmt.Fprintf(&b, " last=%s", panel.LastEvent)
	}
	for _, line := range panel.JournalTail {
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

// renderSummaryLine renders the one-line fleet summary.
func renderSummaryLine(sum Summary) string {
	return fmt.Sprintf("sessions=%d lanes_busy=%d stalled=%d", sum.SessionCount, sum.LanesBusy, sum.StalledCount)
}

// RenderSnapshotJSON marshals snap as the `--once --json` payload (06
// §5.8 automation parity) - a real, complete encoding of every panel,
// never a placeholder subset.
func RenderSnapshotJSON(snap TopSnapshot) ([]byte, error) {
	return json.Marshal(snap)
}
