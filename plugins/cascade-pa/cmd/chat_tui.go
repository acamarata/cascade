package cmd

// Purpose (this file): `cascade chat`'s interactive bubbletea Model, built
//
//	purely from message values so chat_tui_test.go drives Update/View
//	directly — no tea.Program, no TTY, no real clock — matching
//	internal/fleet/top.go's established pattern exactly (R-21.268 pins
//	the same bubbletea/lipgloss versions that ticket already put in
//	go.mod; this file reuses them unchanged, no second TUI framework).
//
// Inputs: an initial thread id and a stream of tea.Msg values (key
//
//	presses, window-size changes, streamed tokens, stream completion/
//	errors). No Clock is injected: unlike internal/fleet/top.go, nothing
//	in this Model performs a temporal operation (no timestamping, no
//	TTL), so adding one would be structure without a caller — the exact
//	thing R-16.79/Art.1 warn against. A future ticket that needs one adds
//	it then.
//
// Outputs: View()'s rendered frame; Model.Err() after Ctrl-D/:q (nil on a
//
//	clean exit).
//
// Constraints: the Model never reads a bare clock, the daemon, or the
//
//	terminal itself — chat_tui_run.go's pump goroutine is the one
//	production caller that turns real Client.Stream activity into
//	messages sent via tea.Program.Send, matching cmd/cascade/
//	fleet_top.go's pumpFleetTopSSE precedent for internal/fleet.Model.
//
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3.

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// chatRenderer is pinned to termenv.Ascii and written to io.Discard —
// exactly internal/fleet/top_render.go's topRenderer pattern — so styles
// derived from it never emit ANSI codes and View()'s frames are
// byte-deterministic in tests regardless of the terminal chat_tui_test.go
// runs under.
var chatRenderer = lipgloss.NewRenderer(nopWriter{}, termenv.WithProfile(termenv.Ascii))

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

var (
	userStyle      = chatRenderer.NewStyle().Bold(true)
	assistantStyle = chatRenderer.NewStyle()
	errStyle       = chatRenderer.NewStyle().Bold(true)
	inputStyle     = chatRenderer.NewStyle()
)

// historySize bounds the in-process input-history ring buffer. Not
// persisted across invocations — Ctrl-P/Ctrl-N only ever navigate what
// this process itself has submitted, by design (07-CLI-COMMAND-TREE.md's
// chat keybindings do not ask for cross-session recall).
const historySize = 50

// inputHistory is the Ctrl-P/Ctrl-N ring buffer: entries are appended on
// Enter and never evicted below historySize; pos tracks the current
// navigation offset (len(entries) means "not navigating, live input").
type inputHistory struct {
	entries []string
	pos     int
}

func (h *inputHistory) push(s string) {
	if s == "" {
		return
	}
	h.entries = append(h.entries, s)
	if len(h.entries) > historySize {
		h.entries = h.entries[len(h.entries)-historySize:]
	}
	h.pos = len(h.entries)
}

func (h *inputHistory) prev() (string, bool) {
	if h.pos <= 0 {
		return "", false
	}
	h.pos--
	return h.entries[h.pos], true
}

func (h *inputHistory) next() (string, bool) {
	if h.pos >= len(h.entries)-1 {
		h.pos = len(h.entries)
		return "", h.pos == len(h.entries)
	}
	h.pos++
	return h.entries[h.pos], true
}

// tokenMsg carries one streamed token into the Model.
type tokenMsg struct{ text string }

// streamDoneMsg reports that the current stream ended without error.
type streamDoneMsg struct{}

// streamErrMsg reports that the current stream ended with err (non-nil).
// It is NOT fatal to the Model: the error is rendered as a line in the
// transcript and the input prompt returns, matching Ctrl-C's own
// abort-without-exit behavior.
type streamErrMsg struct{ err error }

// Model is `cascade chat`'s bubbletea model.
type Model struct {
	thread    string
	lines     []string
	input     string
	history   inputHistory
	streaming bool
	cancel    func()
	width     int
	height    int
	quitting  bool
	err       error
	submit    func(prompt string) (tea.Cmd, func())
}

// NewModel builds a Model for thread (may be "") and submit — the
// callback RunTUI wires to a real Client.Stream call. submit returns the
// tea.Cmd that starts pumping tokenMsg/streamDoneMsg/streamErrMsg values
// into the running Program, plus a cancel func the Model stores and calls
// on a mid-stream Ctrl-C. Tests pass a fake that never touches the
// network.
func NewModel(thread string, submit func(prompt string) (tea.Cmd, func())) Model {
	return Model{thread: thread, submit: submit, width: 80, height: 24}
}

// Err returns the reason the Model quit due to a stream/daemon failure
// severe enough that RunTUI should exit non-zero, or nil on a clean
// Ctrl-D/:q exit.
func (m Model) Err() error { return m.err }

// Init implements tea.Model. No startup command: RunTUI's pump goroutine
// sends the first tokenMsg/streamDoneMsg values in via Program.Send.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(v)
	case tokenMsg:
		if len(m.lines) == 0 {
			m.lines = append(m.lines, assistantStyle.Render(v.text))
		} else {
			m.lines[len(m.lines)-1] += v.text
		}
		return m, nil
	case streamDoneMsg:
		m.streaming = false
		m.cancel = nil
		return m, nil
	case streamErrMsg:
		m.streaming = false
		m.cancel = nil
		m.lines = append(m.lines, errStyle.Render("error: "+v.err.Error()))
		return m, nil
	}
	return m, nil
}

func (m Model) updateKey(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.String() {
	case "ctrl+c":
		if m.streaming && m.cancel != nil {
			m.cancel()
			m.streaming = false
			m.cancel = nil
		}
		return m, nil
	case "ctrl+d":
		m.quitting = true
		return m, tea.Quit
	case "ctrl+p":
		if s, ok := m.history.prev(); ok {
			m.input = s
		}
		return m, nil
	case "ctrl+n":
		if s, ok := m.history.next(); ok {
			m.input = s
		} else {
			m.input = ""
		}
		return m, nil
	case "enter":
		return m.submitInput()
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	}
	if v.Type == tea.KeyRunes {
		m.input += string(v.Runes)
	}
	return m, nil
}

func (m Model) submitInput() (tea.Model, tea.Cmd) {
	prompt := m.input
	if prompt == ":q" {
		m.quitting = true
		return m, tea.Quit
	}
	if prompt == "" || m.streaming {
		return m, nil
	}
	m.history.push(prompt)
	m.lines = append(m.lines, userStyle.Render("> "+prompt))
	m.lines = append(m.lines, "")
	m.input = ""
	m.streaming = true
	if m.submit == nil {
		return m, nil
	}
	cmd, cancel := m.submit(prompt)
	m.cancel = cancel
	return m, cmd
}

// View implements tea.Model.
func (m Model) View() string {
	body := ""
	if len(m.lines) > 0 {
		body = joinLines(m.lines) + "\n"
	}
	return body + inputStyle.Render("> "+m.input)
}

func joinLines(lines []string) string {
	out := lines[0]
	for _, l := range lines[1:] {
		out += "\n" + l
	}
	return out
}
