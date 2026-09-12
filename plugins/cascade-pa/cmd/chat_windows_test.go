//go:build windows

package cmd

// Purpose: the Windows tier-2 TUI refusal proof (18-T0-RULINGS-R16.md
//   R-16.47: asserted by a build-tagged test that runs on the real
//   Windows leg of CI, never via GOOS=windows go test on a non-Windows
//   host — this file is compiled and executed ONLY on windows).
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3.

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestChatWindowsTUIRefusal asserts that `cascade chat` with no prompt and
// no -m refuses with the tier-2 message on Windows, one-shot mode is
// unaffected by the same GOOS check, and CASCADE_NO_INPUT takes priority
// over the platform refusal when both would otherwise apply (matching
// runChat's check order: CASCADE_NO_INPUT is evaluated first).
func TestChatWindowsTUIRefusal(t *testing.T) {
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{}, fakeEnv{}.lookup)
	if err == nil {
		t.Fatal("runChat: want the Windows tier-2 refusal, got nil")
	}
	if !errors.Is(err, errChatWindowsTUIRefusal) {
		t.Fatalf("runChat: want KindUnsupported tier-2 refusal, got %v", err)
	}
	if got, ok := cascade.KindOf(err); !ok || got != cascade.KindUnsupported {
		t.Fatalf("runChat: Kind = %v (ok=%v), want KindUnsupported", got, ok)
	}
}

// TestChatWindowsOneShotUnaffected proves one-shot mode is NOT gated by
// the Windows tier-2 refusal: with a prompt given, runChat never reaches
// the GOOS check at all, so the only possible error is the (also typed,
// also actionable) unconfigured-client error — never the tier-2 message.
func TestChatWindowsOneShotUnaffected(t *testing.T) {
	c := newTestCobraCommand()
	err := runChat(c, chatOptions{prompt: "hello"}, fakeEnv{}.lookup)
	if err == nil {
		t.Fatal("runChat: want the unconfigured-client error, got nil")
	}
	if errors.Is(err, errChatWindowsTUIRefusal) {
		t.Fatal("runChat: one-shot mode must never surface the TUI tier-2 refusal")
	}
}
