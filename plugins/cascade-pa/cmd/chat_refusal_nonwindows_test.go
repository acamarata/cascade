//go:build !windows

// Purpose: TestChatTUIRefusesWhenDaemonUnreachable, split out of
//
//	chat_test.go (P1 windows-CI fix; see journals/FIX-windows-sync-
//	migration-tui.md) because on windows, runChat's no-prompt path
//	refuses unconditionally with the tier-2 message
//	(errChatWindowsTUIRefusal, chat.go) BEFORE it ever reaches the
//	unconfigured-Client check this test asserts (see runChat's ordering:
//	the goruntime.GOOS=="windows" check runs before runTUI, and runTUI
//	itself, and hence errClientUnconfigured, is never reached). This is
//	not a Windows regression to fix: it is the same tier-2 refusal
//	TestChatWindowsTUIRefusal (chat_windows_test.go) already proves
//	deliberately fires first there. The windows-side behavior this test
//	cannot exercise is proven instead by TestChatWindowsTUIRefusal and
//	TestRunTUIWindowsTierTwoRefusal (both chat_windows_test.go).
//
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3.
package cmd

import (
	"context"
	"errors"
	"testing"
)

// TestChatTUIRefusesWhenDaemonUnreachable: `cascade chat` with no prompt
// and an unconfigured Client refuses immediately with a typed error
// WITHOUT ever constructing a tea.Program — proof there is no blank
// screen, no hang, and no silent retry loop. This is the TTY-free proof
// for the "no arg" path: runChat never reaches tea.NewProgram in this
// case, so the test needs no TTY and completes instantly.
func TestChatTUIRefusesWhenDaemonUnreachable(t *testing.T) {
	resetClient(t)
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{}, fakeEnv{}.lookup)
	if !errors.Is(err, errClientUnconfigured) {
		t.Fatalf("runChat: err = %v, want errClientUnconfigured", err)
	}
}

// TestRunTUIRefusesUnconfiguredClient proves runTUI itself (not just
// runChat's dispatch to it) refuses immediately, with no tea.Program ever
// constructed — this call would hang on a real terminal loop if the
// preflight check were missing, so a passing, fast test IS the proof.
func TestRunTUIRefusesUnconfiguredClient(t *testing.T) {
	resetClient(t)
	c := newTestCobraCommand()
	err := runTUI(context.Background(), c, "")
	if !errors.Is(err, errClientUnconfigured) {
		t.Fatalf("runTUI: err = %v, want errClientUnconfigured", err)
	}
}
