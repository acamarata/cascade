package cmd

// Purpose: continuation of chat_tui_test.go, split to satisfy the
//   repo's 300-line-per-file cap (test files included). Same doubles,
//   same no-tea.Program/no-TTY/no-real-clock discipline.

import (
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

// TestChatModelViewMultipleLines exercises joinLines's multi-line path.
func TestChatModelViewMultipleLines(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.lines = []string{"one", "two", "three"}
	got := m.View()
	want := "one\ntwo\nthree\n> "
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}

// TestChatModelWindowSizeMsg exercises the WindowSizeMsg branch.
func TestChatModelWindowSizeMsg(t *testing.T) {
	m := NewModel("", noopSubmit)
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	nm := next.(Model)
	if nm.width != 100 || nm.height != 40 {
		t.Fatalf("width/height = %d/%d, want 100/40", nm.width, nm.height)
	}
	if cmd != nil {
		t.Fatal("WindowSizeMsg must not return a command")
	}
}

// TestChatModelStreamDoneClearsStreaming exercises the streamDoneMsg
// branch directly.
func TestChatModelStreamDoneClearsStreaming(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.streaming = true
	next, _ := m.Update(streamDoneMsg{})
	nm := next.(Model)
	if nm.streaming {
		t.Fatal("streaming = true after streamDoneMsg, want false")
	}
}

// TestChatModelUnhandledMsgIsNoop exercises Update's default fallthrough
// for a message type the Model does not recognize.
func TestChatModelUnhandledMsgIsNoop(t *testing.T) {
	m := NewModel("", noopSubmit)
	type unknownMsg struct{}
	next, cmd := m.Update(unknownMsg{})
	if cmd != nil {
		t.Fatal("unhandled message must not return a command")
	}
	if _, ok := next.(Model); !ok {
		t.Fatal("unhandled message must still return a Model")
	}
}

// TestChatModelRuneAndBackspace exercises rune insertion and backspace,
// including backspace on an already-empty input (a no-op, not a panic).
func TestChatModelRuneAndBackspace(t *testing.T) {
	m := NewModel("", noopSubmit)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	m = next.(Model)
	if m.input != "ab" {
		t.Fatalf("input = %q, want %q", m.input, "ab")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.input != "a" {
		t.Fatalf("input after backspace = %q, want %q", m.input, "a")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.input != "" {
		t.Fatalf("backspace on empty input must be a no-op, got %q", m.input)
	}
}

// TestChatModelEnterOnEmptyInputIsNoop: Enter with no input does nothing
// (no transcript line, not streaming).
func TestChatModelEnterOnEmptyInputIsNoop(t *testing.T) {
	m := NewModel("", noopSubmit)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if cmd != nil || nm.streaming || len(nm.lines) != 0 {
		t.Fatal("Enter on empty input must be a complete no-op")
	}
}

// TestChatModelEnterWhileStreamingIsNoop: Enter is ignored while a
// previous turn is still streaming.
func TestChatModelEnterWhileStreamingIsNoop(t *testing.T) {
	m := NewModel("", noopSubmit)
	m.streaming = true
	m.input = "another"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if nm.input != "another" {
		t.Fatal("Enter while streaming must not consume the input")
	}
}

// TestChatModelCtrlPOnEmptyHistoryIsNoop and TestChatModelInitAndNopWriter
// round out the remaining trivial branches: Init's nil tea.Cmd and
// nopWriter's Write (chatRenderer's sink).
func TestChatModelCtrlPOnEmptyHistoryIsNoop(t *testing.T) {
	m := NewModel("", noopSubmit)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	nm := next.(Model)
	if nm.input != "" {
		t.Fatalf("Ctrl-P on empty history: input = %q, want empty", nm.input)
	}
}

func TestChatModelInitAndNopWriter(t *testing.T) {
	m := NewModel("", noopSubmit)
	if m.Init() != nil {
		t.Fatal("Init() must return nil")
	}
	n, err := (nopWriter{}).Write([]byte("xyz"))
	if err != nil || n != 3 {
		t.Fatalf("nopWriter.Write = %d, %v, want 3, nil", n, err)
	}
}

// TestInputHistoryWraparound exercises the ring buffer's eviction once it
// exceeds historySize, and confirms push("") is a documented no-op.
func TestInputHistoryWraparound(t *testing.T) {
	var h inputHistory
	h.push("")
	if len(h.entries) != 0 {
		t.Fatal("push(\"\") must be a no-op")
	}
	for i := 0; i < historySize+5; i++ {
		h.push(string(rune('a' + i%26)))
	}
	if len(h.entries) != historySize {
		t.Fatalf("len(entries) = %d, want %d after wraparound", len(h.entries), historySize)
	}
}
