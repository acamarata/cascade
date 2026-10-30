//go:build integration

// Purpose (this file): bridge_acceptance_integration_test.go's own TG->CLI
//   half, split out to stay under Art.10.3's 300-line cap — R10's "asks the
//   owner (locally) to reply in the DM, polls up to 10 minutes, and asserts
//   AppendTurn on the SAME thread with Origin/Untrusted pinned. With
//   credentials present the reply leg FAILS on timeout" (no skip).
//
// SPORT: plugins/cascade-pa (external, integration-tagged)
//   TestPABridge_OwnerWindow/TEST (P1-E23-W5-S49-T4).

package cascadepa_test

import (
	"context"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// runReplyRoundtrip is TestPABridge_OwnerWindow's TG->CLI half.
func runReplyRoundtrip(ctx context.Context, t *testing.T, bridge *telegram.TelegramBridge,
	chatSvc *integrationChatService, threadID string) {
	t.Helper()
	nonce := randomNonce(t)
	prompt := []byte("cascade acceptance: reply with " + nonce)
	bridgeSendMirrored(ctx, t, threadID, string(prompt))
	t.Logf("ACTION NEEDED: reply to that DM with the nonce %s within %s", nonce, replyWaitTimeout)

	ch, err := bridge.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	deadline := time.After(replyWaitTimeout)
	for {
		select {
		case msg := <-ch:
			if msg.Origin != cascadepa.OriginBridgeTelegram || !msg.Untrusted {
				t.Fatalf("delivered message carries Origin=%q Untrusted=%v, want %q/true",
					msg.Origin, msg.Untrusted, cascadepa.OriginBridgeTelegram)
			}
			if msg.ThreadID != threadID {
				continue // a delivery on a different thread is not this run's reply
			}
			if !strings.Contains(string(msg.Body), nonce) {
				continue // a different DM arrived first; keep waiting for the nonce
			}
			assertReplyAppended(t, chatSvc, threadID, nonce)
			t.Log("TG->CLI leg proven: the owner's DM reply reached the real admission path and was appended")
			return
		case <-deadline:
			t.Fatalf("no reply containing the nonce arrived within %s (credentials were present; this is a failure, not a skip)",
				replyWaitTimeout)
		case <-ctx.Done():
			t.Fatalf("context ended before a reply arrived: %v", ctx.Err())
		}
	}
}

// assertReplyAppended checks chat.append_turn (via the integration
// ChatService recorder) actually recorded the nonce on threadID -- the
// contract's own "asserts that message appears in `cascade chat` for the
// bridged thread", proven against what chat.go's forward() called, not
// merely against what Receive delivered.
func assertReplyAppended(t *testing.T, chatSvc *integrationChatService, threadID, nonce string) {
	t.Helper()
	chatSvc.mu.Lock()
	defer chatSvc.mu.Unlock()
	for _, call := range chatSvc.calls {
		if call.threadID == threadID && strings.Contains(call.text, nonce) {
			return
		}
	}
	t.Fatalf("AppendTurn was never called on the owner thread with the nonce (%d turns recorded)", len(chatSvc.calls))
}
