package fleet

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestFleetTopSSEReplay drives FoldSSE from the recorded, real-handler
// SSE fixture (internal/fleet/testdata/top-sse-fixture.ndjson, provenance
// in that directory's README.md) — no daemon, no network, no httptest
// server at test time. Required by this ticket's own `checks` list
// (-run TestFleetTopSSEReplay).
func TestFleetTopSSEReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/top-sse-fixture.ndjson")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	clk := runtime.NewFixedClock(time.Unix(1_700_000_100, 0))
	final := FoldSSE(strings.NewReader(string(data)), TopSnapshot{}, clk, nil)

	if len(final.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2 (sess-alpha, sess-beta); got %+v", len(final.Sessions), final.Sessions)
	}
	byID := map[string]TopSessionRow{}
	for _, s := range final.Sessions {
		byID[s.SessionID] = s
	}
	alpha, ok := byID["sess-alpha"]
	if !ok {
		t.Fatalf("sess-alpha missing from folded snapshot: %+v", final.Sessions)
	}
	// The fixture's third event re-states sess-alpha as "completed" —
	// FoldSSE must have applied it as an UPDATE, not a duplicate insert.
	if alpha.State != "completed" {
		t.Fatalf("sess-alpha.State = %q, want %q (last event wins)", alpha.State, "completed")
	}
	beta, ok := byID["sess-beta"]
	if !ok || beta.State != "stalled" {
		t.Fatalf("sess-beta = %+v, want State=stalled", beta)
	}
	if final.Summary.SessionCount != 2 || final.Summary.StalledCount != 1 {
		t.Fatalf("Summary = %+v, want SessionCount=2 StalledCount=1", final.Summary)
	}
}

func TestFoldSSE_MalformedBlockSkipped(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	raw := "event: fleet.sessions.changed\nid: 1\ndata: not-json\nretry: 3000\n\n" +
		"event: fleet.sessions.changed\nid: 2\ndata: {\"session_id\":\"s1\",\"state\":\"running\"}\nretry: 3000\n\n"
	final := FoldSSE(strings.NewReader(raw), TopSnapshot{}, clk, nil)
	if len(final.Sessions) != 1 || final.Sessions[0].SessionID != "s1" {
		t.Fatalf("expected exactly one folded session, got %+v", final.Sessions)
	}
}

// TestFoldSSE_OnRowCallback proves the onRow callback (cmd/cascade's
// real production wiring path) fires once per decoded row, in order,
// with the same rows the returned snapshot ends up holding.
func TestFoldSSE_OnRowCallback(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	raw := "data: {\"session_id\":\"s1\",\"state\":\"running\"}\n\n" +
		"data: {\"session_id\":\"s2\",\"state\":\"stalled\"}\n\n"
	var got []string
	final := FoldSSE(strings.NewReader(raw), TopSnapshot{}, clk, func(r TopSessionRow) {
		got = append(got, r.SessionID)
	})
	if len(got) != 2 || got[0] != "s1" || got[1] != "s2" {
		t.Fatalf("onRow calls = %v, want [s1 s2]", got)
	}
	if len(final.Sessions) != 2 {
		t.Fatalf("final.Sessions = %+v, want 2 rows", final.Sessions)
	}
}

func TestModel_ResizeAndView(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	m := NewModel(NewTopSnapshot(nil, GovernorPanel{}, TaskPanel{}, clk.Now()), clk)
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd != nil {
		t.Fatalf("expected nil cmd on resize, got %v", cmd)
	}
	view := next.View()
	if !strings.Contains(view, "Sessions") {
		t.Fatalf("View() missing Sessions panel: %s", view)
	}
}

func TestModel_QuitOnQ(t *testing.T) {
	m := NewModel(TopSnapshot{}, runtime.NewFixedClock(time.Unix(0, 0)))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatalf("expected tea.Quit cmd on 'q'")
	}
	if !next.(Model).quitting {
		t.Fatalf("expected quitting=true after 'q'")
	}
}

func TestModel_QuitOnCtrlC(t *testing.T) {
	m := NewModel(TopSnapshot{}, runtime.NewFixedClock(time.Unix(0, 0)))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected tea.Quit cmd on Ctrl-C")
	}
	if !next.(Model).quitting {
		t.Fatalf("expected quitting=true after Ctrl-C")
	}
}

func TestModel_SessionEventMsg_UpdatesView(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	m := NewModel(TopSnapshot{}, clk)
	next, _ := m.Update(SessionEventMsg{Row: TopSessionRow{SessionID: "s9", State: "running"}})
	view := next.View()
	if !strings.Contains(view, "s9") {
		t.Fatalf("View() missing new session row: %s", view)
	}
}

func TestModel_DisconnectMsg_QuitsWithDescriptiveError(t *testing.T) {
	m := NewModel(TopSnapshot{}, runtime.NewFixedClock(time.Unix(0, 0)))
	wantErr := errors.New("connection reset")
	next, cmd := m.Update(DisconnectMsg{Err: wantErr})
	if cmd == nil {
		t.Fatalf("expected tea.Quit cmd on disconnect")
	}
	nm := next.(Model)
	if nm.Disconnected() != wantErr {
		t.Fatalf("Disconnected() = %v, want %v", nm.Disconnected(), wantErr)
	}
	if !strings.Contains(nm.View(), "daemon disconnected: connection reset") {
		t.Fatalf("View() not a descriptive disconnect error: %s", nm.View())
	}
}

func TestModel_SnapshotMsg(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	m := NewModel(TopSnapshot{}, clk)
	newSnap := NewTopSnapshot([]TopSessionRow{{SessionID: "s1", State: "running"}}, GovernorPanel{}, TaskPanel{}, clk.Now())
	next, cmd := m.Update(SnapshotMsg{Snapshot: newSnap})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if !strings.Contains(next.View(), "s1") {
		t.Fatalf("View() missing s1 after SnapshotMsg: %s", next.View())
	}
}
