// Purpose (this file): TelegramBridge's behavior — the §5.16 privacy matrix (REFUSE local-only/restricted/
//   unresolvable, ALLOW internal/public), S-48.T1/T3 gate ordering, Untrusted/Origin (R-21.227), lifecycle.
//
// SPORT: plugins/cascade-pa/telegram TestBridgePrivacy*/TEST, TestInboundMessageUntrustedOrigin/TEST (P1-E23-W5-S48-T2).

package telegram

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// bridgeRig is a testRig (rig_test.go) plus a TelegramBridge over fake ChatService/ThreadPrivacyResolver/DivergenceSink seams.
type bridgeRig struct {
	*testRig
	bridge     *TelegramBridge
	privacy    *fakeThreadPrivacy
	chat       *fakeChatService
	divergence *fakeDivergenceSink
}

func newBridgeRig(t *testing.T) *bridgeRig {
	t.Helper()
	rig := newDefaultRig(t)
	privacy := newFakeThreadPrivacy()
	chat := &fakeChatService{}
	divergence := &fakeDivergenceSink{}
	bridge := NewTelegramBridge(rig.module, rig.stores.Binding, testSubject, chat, privacy, divergence)
	return &bridgeRig{testRig: rig, bridge: bridge, privacy: privacy, chat: chat, divergence: divergence}
}

// dispatchText runs one message through the FULL production dispatch path
// (binding, secret-scan, elevated-verb, handleInbound); senderID is decimal.
func (r *bridgeRig) dispatchText(t *testing.T, senderID string, chatID int64, text string) {
	t.Helper()
	fromID, err := strconv.ParseInt(senderID, 10, 64)
	if err != nil {
		t.Fatalf("senderID %q is not numeric: %v", senderID, err)
	}
	r.module.dispatch(context.Background(), textUpdate(1, fromID, chatID, text))
}

// TestBridgePrivacy_RefusalMatrix covers the three REFUSE paths (task's
// security test matrix, Art.4): local-only, restricted, and an
// unresolvable tier (§5.16 fail-closed: follows the local-only path). In
// every case no message content exits and exactly one divergence event
// and one RefusalReport entry are recorded.
func TestBridgePrivacy_RefusalMatrix(t *testing.T) {
	cases := []struct {
		name       string
		setTier    bool
		tier       cascadepa.SensitivityTier
		resolveErr error
		wantReason string
		wantTier   cascadepa.SensitivityTier
	}{
		{"local-only", true, cascadepa.TierLocalOnly, nil, reasonThreadLocalOnly, cascadepa.TierLocalOnly},
		{"restricted", true, cascadepa.TierRestricted, nil, reasonThreadRestricted, cascadepa.TierRestricted},
		{"unresolvable", false, "", errTestGateClosed, reasonThreadLocalOnly, cascadepa.TierLocalOnly},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newBridgeRig(t)
			senderID := strconv.Itoa(6000 + i)
			chatID := int64(9100 + i)
			r.bind(t, senderID)
			threadID := threadIDForChat(chatID)
			if c.setTier {
				r.privacy.set(threadID, c.tier)
			}
			r.privacy.err = c.resolveErr

			r.dispatchText(t, senderID, chatID, "secret plan")

			if n := r.chat.callCount(); n != 0 {
				t.Fatalf("AppendTurn called %d times, want 0 (no message content exits)", n)
			}
			sent := lastSent(t, r.doer)
			if sent != c.wantReason {
				t.Fatalf("reply = %q, want %q", sent, c.wantReason)
			}
			if strings.Contains(sent, "secret plan") {
				t.Fatal("refusal reply carries the original message content")
			}
			events := r.divergence.snapshot()
			if len(events) != 1 || events[0].ResolvedTier != string(c.wantTier) || events[0].ThreadID != threadID {
				t.Fatalf("divergence events = %+v, want one bridge.refused for %s/%s", events, threadID, c.wantTier)
			}
			if got := r.bridge.RefusalReport(); len(got) != 1 || got[0].Reason != c.wantReason {
				t.Fatalf("RefusalReport = %+v", got)
			}
		})
	}
}

func TestBridgePrivacy_InternalForwardsAndReplies(t *testing.T) {
	r := newBridgeRig(t)
	r.bind(t, "5554")
	threadID := threadIDForChat(9004)
	r.privacy.set(threadID, cascadepa.TierInternal)

	r.dispatchText(t, "5554", 9004, "hello world")

	if n := r.chat.callCount(); n != 1 {
		t.Fatalf("AppendTurn called %d times, want 1", n)
	}
	call := r.chat.calls[0]
	if call.threadID != threadID || call.role != "user" || call.text != "hello world" {
		t.Fatalf("AppendTurn call = %+v, want thread %s/user/%q", call, threadID, "hello world")
	}
	sent := lastSent(t, r.doer)
	if !strings.Contains(sent, threadID) || !strings.Contains(sent, "turn-1") {
		t.Fatalf("reply = %q, want it to name the thread and turn", sent)
	}
	if tiers := r.gate.observedTiers(); len(tiers) == 0 || tiers[len(tiers)-1] != cascadepa.TierInternal {
		t.Fatalf("observed tiers = %v, want the final Guard call at TierInternal", tiers)
	}
	if len(r.divergence.snapshot()) != 0 {
		t.Fatal("an ALLOW path must publish no divergence event")
	}
}

func TestBridgePrivacy_PublicForwardsAndReplies(t *testing.T) {
	r := newBridgeRig(t)
	r.bind(t, "5555")
	threadID := threadIDForChat(9005)
	r.privacy.set(threadID, cascadepa.TierPublic)

	r.dispatchText(t, "5555", 9005, "hello")

	if n := r.chat.callCount(); n != 1 {
		t.Fatalf("AppendTurn called %d times, want 1", n)
	}
	if tiers := r.gate.observedTiers(); len(tiers) == 0 || tiers[len(tiers)-1] != cascadepa.TierPublic {
		t.Fatalf("observed tiers = %v, want the final Guard call at TierPublic", tiers)
	}
}

// TestBridgePrivacy_UnpairedSenderRefusedBeforeEgress proves the S-48.T1 binding gate runs ahead of this ticket's own privacy gate.
func TestBridgePrivacy_UnpairedSenderRefusedBeforeEgress(t *testing.T) {
	r := newBridgeRig(t)
	threadID := threadIDForChat(9006)
	r.privacy.set(threadID, cascadepa.TierPublic) // would ALLOW, if ever reached

	r.dispatchText(t, "999999", 9006, "hello")

	if n := r.chat.callCount(); n != 0 {
		t.Fatalf("AppendTurn called %d times for an unpaired sender, want 0", n)
	}
	if sent := lastSent(t, r.doer); sent != replyNotPaired {
		t.Fatalf("reply = %q, want %q", sent, replyNotPaired)
	}
}

// TestInboundMessageUntrustedOrigin proves R-21.227: an ALLOW-path forward is delivered on Receive with Untrusted=true, Origin=bridge-telegram.
func TestInboundMessageUntrustedOrigin(t *testing.T) {
	r := newBridgeRig(t)
	r.bind(t, "5556")
	threadID := threadIDForChat(9007)
	r.privacy.set(threadID, cascadepa.TierInternal)
	ch, err := r.bridge.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	r.dispatchText(t, "5556", 9007, "carrying content")

	select {
	case delivered := <-ch:
		if !delivered.Untrusted {
			t.Fatal("delivered.Untrusted = false, want true (R-21.227: never clearable)")
		}
		if delivered.Origin != cascadepa.OriginBridgeTelegram {
			t.Fatalf("delivered.Origin = %q, want %q", delivered.Origin, cascadepa.OriginBridgeTelegram)
		}
		if delivered.ThreadID != threadID {
			t.Fatalf("delivered.ThreadID = %q, want %q", delivered.ThreadID, threadID)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing delivered on Receive within 1s")
	}
}

func TestChatBridge_Paired(t *testing.T) {
	r := newBridgeRig(t)
	if r.bridge.Paired() {
		t.Fatal("Paired() = true before any bind")
	}
	r.bind(t, "5557")
	if !r.bridge.Paired() {
		t.Fatal("Paired() = false after Bind")
	}
}

// TestChatBridge_StartStop proves Start/Stop delegate to the real module.
func TestChatBridge_StartStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Start is refused on Windows tier-2")
	}
	r := newBridgeRig(t)
	r.doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	if err := r.bridge.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.bridge.Stop(ctx); err != nil {
		t.Fatalf("Stop did not drain: %v", err)
	}
	if _, ok := <-r.bridge.inbound; ok {
		t.Fatal("the Receive channel is still open after Stop")
	}
}

func TestChatBridge_Send(t *testing.T) {
	t.Run("unknown thread refuses", func(t *testing.T) {
		r := newBridgeRig(t)
		if err := r.bridge.Send(context.Background(), "not-a-bridge-thread", []byte("x")); err == nil {
			t.Fatal("Send on a foreign thread id succeeded, want a refusal")
		}
		if len(r.doer.methods()) != 0 {
			t.Fatal("a refused Send still reached the transport")
		}
	})
	t.Run("resolves tier and sends", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Send is refused on Windows tier-2; TestBridgeWindowsTierTwoRefusal_Send proves that path")
		}
		r := newBridgeRig(t)
		threadID := threadIDForChat(9008)
		r.privacy.set(threadID, cascadepa.TierPublic)
		if err := r.bridge.Send(context.Background(), threadID, []byte("assistant reply")); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if sent := lastSent(t, r.doer); sent != "assistant reply" {
			t.Fatalf("sent text = %q, want %q", sent, "assistant reply")
		}
		if tiers := r.gate.observedTiers(); len(tiers) == 0 || tiers[len(tiers)-1] != cascadepa.TierPublic {
			t.Fatalf("observed tiers = %v, want the send at TierPublic", tiers)
		}
	})
}

func TestChatBridge_AppendTurnFailure_RepliesWithoutThreadContent(t *testing.T) {
	r := newBridgeRig(t)
	r.bind(t, "5558")
	threadID := threadIDForChat(9009)
	r.privacy.set(threadID, cascadepa.TierInternal)
	r.chat.err = errTestStoreDown

	r.dispatchText(t, "5558", 9009, "will not be recorded")

	sent := lastSent(t, r.doer)
	if strings.Contains(sent, "will not be recorded") {
		t.Fatal("a chat-service failure reply carries the original message content")
	}
	if sent == "" {
		t.Fatal("no reply sent on an AppendTurn failure")
	}
}

// TestChatBridge_Drain_WaitsForInFlight proves Drain blocks on a real
// in-flight handleInbound call: a mutation dropping inFlight.Add(1)/Add(-1)
// would make Drain return at once, which this test would catch.
func TestChatBridge_Drain_WaitsForInFlight(t *testing.T) {
	r := newBridgeRig(t)
	r.bind(t, "5559")
	threadID := threadIDForChat(9010)
	r.privacy.set(threadID, cascadepa.TierInternal)
	r.chat.block = make(chan struct{})

	// Direct dispatch (not dispatchText, whose t.Fatalf must stay on this
	// goroutine): the sender id is a numeric literal, nothing to parse.
	go r.module.dispatch(context.Background(), textUpdate(1, 5559, 9010, "in flight"))
	time.Sleep(20 * time.Millisecond) // let dispatch reach the blocked AppendTurn call

	drainDone := make(chan error, 1)
	go func() { drainDone <- r.bridge.Drain(context.Background()) }()
	select {
	case <-drainDone:
		t.Fatal("Drain returned before the in-flight handler finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(r.chat.block)
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Drain did not return after the in-flight handler finished")
	}
}
