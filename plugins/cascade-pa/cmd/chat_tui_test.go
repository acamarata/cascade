package cmd

// Purpose: drives Model.Update/View directly by message value — no
//   tea.Program, no TTY, no real clock, matching internal/fleet/
//   top_test.go's established pattern for this repo's TUI models.

import (
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

func noopSubmit(string) (tea.Cmd, func()) { return nil, func() {} }

// TestChatModelEnterSubmitsAndStreams: Enter on non-empty input appends a
// user line, clears the input, marks streaming, and invokes submit.
func TestChatModelEnterSubmitsAndStreams(t *testing.T) {
	var submitted string
	m := NewModel("", func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return nil, func() {}
	})
	m.input = "hello"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if submitted != "hello" {
		t.Fatalf("submit prompt = %q, want %q", submitted, "hello")
	}
	if nm.input != "" {
		t.Fatalf("input = %q, want empty after submit", nm.input)
	}
	if !nm.streaming {
		t.Fatal("streaming = false, want true after submit")
	}
	if len(nm.lines) == 0 {
		t.Fatal("no transcript line recorded for the submitted prompt")
	}
}

// TestChatModelTokenAccumulation: streamed tokens append onto the last
// transcript line rather than each starting a new one.
func TestChatModelTokenAccumulation(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.input = "hi"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	next, _ = m.Update(tokenMsg{text: "Hel"})
	m = next.(Model)
	next, _ = m.Update(tokenMsg{text: "lo"})
	m = next.(Model)

	last := m.lines[len(m.lines)-1]
	if last != "Hello" {
		t.Fatalf("last transcript line = %q, want %q", last, "Hello")
	}
}

// TestChatModelCtrlCAbortsWithoutExiting: Ctrl-C mid-stream calls the
// stream's cancel func, clears streaming, and returns to the input
// prompt WITHOUT quitting the program.
func TestChatModelCtrlCAbortsWithoutExiting(t *testing.T) {
	canceled := false
	m := NewModel("", func(string) (tea.Cmd, func()) {
		return nil, func() { canceled = true }
	})
	m.input = "hi"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.streaming {
		t.Fatal("precondition: expected streaming after submit")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	if !canceled {
		t.Fatal("Ctrl-C did not invoke the stream's cancel func")
	}
	if m.streaming {
		t.Fatal("streaming = true after Ctrl-C, want false")
	}
	if m.quitting {
		t.Fatal("Ctrl-C must return to the input prompt, not quit")
	}
	if cmd != nil {
		t.Fatal("Ctrl-C must not return tea.Quit")
	}
}

// TestChatModelCtrlDExitsCleanly: Ctrl-D quits with no error.
func TestChatModelCtrlDExitsCleanly(t *testing.T) {
	m := NewModel("", noopSubmit)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	nm := next.(Model)
	if !nm.quitting {
		t.Fatal("quitting = false after Ctrl-D, want true")
	}
	if cmd == nil {
		t.Fatal("Ctrl-D must return tea.Quit")
	}
	if nm.Err() != nil {
		t.Fatalf("Err() = %v, want nil after a clean Ctrl-D exit", nm.Err())
	}
}

// TestChatModelColonQExitsCleanly: typing ":q" then Enter quits cleanly,
// exactly like Ctrl-D.
func TestChatModelColonQExitsCleanly(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.input = ":q"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if !nm.quitting || cmd == nil {
		t.Fatal(":q + Enter must quit cleanly")
	}
}

// TestChatModelHistoryRingNavigation: Ctrl-P/Ctrl-N walk the in-process
// input history ring buffer built from prior Enter submissions.
func TestChatModelHistoryRingNavigation(t *testing.T) {
	m := NewModel("", noopSubmit)
	for _, s := range []string{"first", "second"} {
		m.input = s
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(Model)
		// Stream never resolves in this test, so force streaming off to
		// allow the next Enter to submit (submitInput refuses while
		// streaming — the model must return to input to test history).
		m.streaming = false
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = next.(Model)
	if m.input != "second" {
		t.Fatalf("Ctrl-P: input = %q, want %q", m.input, "second")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = next.(Model)
	if m.input != "first" {
		t.Fatalf("Ctrl-P again: input = %q, want %q", m.input, "first")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = next.(Model)
	if m.input != "second" {
		t.Fatalf("Ctrl-N: input = %q, want %q", m.input, "second")
	}
}

// TestChatModelStreamErrRendersWithoutExiting: a streamErrMsg (e.g. a
// malformed SSE frame reported upstream) is rendered into the transcript
// and clears streaming, but never quits the Model.
func TestChatModelStreamErrRendersWithoutExiting(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.streaming = true
	next, _ := m.Update(streamErrMsg{err: errClientUnconfigured})
	m = next.(Model)
	if m.streaming {
		t.Fatal("streaming = true after streamErrMsg, want false")
	}
	if m.quitting {
		t.Fatal("streamErrMsg must not quit the Model")
	}
	if len(m.lines) == 0 {
		t.Fatal("streamErrMsg must render a transcript line")
	}
}

// TestChatModelViewNoTTY proves View() renders deterministically with no
// terminal at all — the byte-deterministic frame requirement.
func TestChatModelViewNoTTY(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.input = "hi"
	got := m.View()
	want := "> hi"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}
