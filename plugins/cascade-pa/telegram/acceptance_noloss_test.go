// Purpose (this file): P1-E23-W5-S49-T4 round-2 fix item 4 (T0 decision
//   R4): a DETERMINISTIC, COUNTED no-loss proof over multiple recorded
//   updates on one thread, through the REAL BotClient.Poll/dispatch chain
//   (not a bare module.dispatch call, which every other acceptance test in
//   this file already uses for R3's decision-seam proof) — the confirming
//   CR's own words: "no counted multi-update proof over recorded updates,
//   no same-thread-as-Send, Origin/Untrusted not asserted in the
//   acceptance."
//
// Inputs: three getUpdates fixtures built with textUpdate (this package's
//   own helper, module_test.go), fed to the REAL fakeDoer queue so this
//   test exercises client.Poll's own JSON decode + ledger dedup + dispatch
//   chain, not a hand-called dispatch.
//
// Outputs: TestPABridge_NoLossAcrossRecordedUpdates: AppendTurn called
//   exactly N times (never fewer — a loss — never more — a replay), each
//   with a distinct update's text, all on the SAME thread as the delivered
//   Receive items, each of which carries Origin=OriginBridgeTelegram and
//   Untrusted=true (R-21.227's provenance stamp, admitMessage's own
//   guarantee — this test proves it end to end rather than assuming it).
//
// Constraints: the real egress engine and detector-backed scanner
//   (acceptRealFirewall) still back the dispatch, per R3 — this file adds
//   the COUNTED, MULTI-UPDATE dimension R3's other tests never covered,
//   it does not relax any decision seam they already proved real.
//
// SPORT: plugins/cascade-pa/telegram
//   TestPABridge_NoLossAcrossRecordedUpdates/TEST (P1-E23-W5-S49-T4).

package telegram

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// noLossReceiveTimeout bounds each individual Receive read: generous for a
// purely in-process, no-network dispatch chain, short enough that a
// regression that drops a message fails fast instead of hanging the suite.
const noLossReceiveTimeout = 3 * time.Second

// TestPABridge_NoLossAcrossRecordedUpdates is R4: N recorded updates on one
// thread, through the real Poll/dispatch chain, produce exactly N AppendTurn
// calls with distinct texts, all on the same thread, each delivered Receive
// item carrying Origin/Untrusted pinned.
func TestPABridge_NoLossAcrossRecordedUpdates(t *testing.T) {
	skipOnWindowsTier2(t)
	const owner = int64(7500)
	r := newAcceptRig(t, "", "")
	threadID := r.pairOwner(t, owner, provider.SensitivityInternal)
	texts := []string{"first update in the run", "second update in the run", "third update in the run"}
	r.doer.push(noLossEnvelope(t, owner, texts), nil)

	ctx, cancel := context.WithTimeout(context.Background(), noLossReceiveTimeout*time.Duration(len(texts)+2))
	defer cancel()
	if err := r.bridge.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ch, err := r.bridge.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	received := drainNoLossReceive(t, ch, len(texts))
	if err := r.bridge.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertNoLossDelivery(t, received, threadID, texts)
	assertNoLossAppendTurns(t, r.chat.fakeChatService, threadID, texts)
	assertSendUsesSameThread(t, r, threadID, owner)
}

// assertSendUsesSameThread: the outbound Send on the thread every turn was
// appended to resolves, through the same map, to the owner's chat.
func assertSendUsesSameThread(t *testing.T, r *acceptRig, threadID string, owner int64) {
	t.Helper()
	before := len(r.sendMessages())
	if err := r.bridge.Send(context.Background(), threadID, []byte("outbound on the same thread")); err != nil {
		t.Fatalf("Send on the appended thread: %v", err)
	}
	sent := r.sendMessages()
	if len(sent) != before+1 || sent[len(sent)-1].ChatID != owner ||
		sent[len(sent)-1].Text != "outbound on the same thread" {
		t.Fatalf("Send did not reach the owner chat of thread %q: %+v", threadID, sent)
	}
}

// drainNoLossReceive reads exactly want items from ch, one at a time, each
// bounded by noLossReceiveTimeout — a genuine loss (fewer than want
// delivered) fails by timing out rather than by silently returning short.
func drainNoLossReceive(t *testing.T, ch <-chan cascadepa.InboundMessage, want int) []cascadepa.InboundMessage {
	t.Helper()
	got := make([]cascadepa.InboundMessage, 0, want)
	for i := 0; i < want; i++ {
		select {
		case msg := <-ch:
			got = append(got, msg)
		case <-time.After(noLossReceiveTimeout):
			t.Fatalf("Receive delivered %d of %d expected messages before timing out (a loss)", len(got), want)
		}
	}
	return got
}

// assertNoLossDelivery checks the COUNTED, PROVENANCE half: exactly
// len(texts) items were delivered, each on threadID, each carrying
// Origin/Untrusted pinned (R-21.227), and their texts are exactly the
// distinct set texts named — no duplicate, no substitution, no drop.
func assertNoLossDelivery(t *testing.T, got []cascadepa.InboundMessage, threadID string, texts []string) {
	t.Helper()
	if len(got) != len(texts) {
		t.Fatalf("delivered %d messages, want exactly %d (no loss, no replay)", len(got), len(texts))
	}
	seen := make(map[string]bool, len(texts))
	for _, msg := range got {
		if msg.ThreadID != threadID {
			t.Fatalf("delivered message thread = %q, want %q (same thread as the recorded updates)", msg.ThreadID, threadID)
		}
		if msg.Origin != cascadepa.OriginBridgeTelegram || !msg.Untrusted {
			t.Fatalf("delivered message = %+v, want Origin=%q Untrusted=true", msg, cascadepa.OriginBridgeTelegram)
		}
		seen[string(msg.Body)] = true
	}
	for _, want := range texts {
		if !seen[want] {
			t.Fatalf("delivered messages never carried text %q: got %+v", want, got)
		}
	}
}

// assertNoLossAppendTurns checks the CHAT-PARITY half: AppendTurn was
// called exactly len(texts) times, all on threadID, one per distinct text
// — the same counted-no-loss property, proven against the recorder chat.go
// forward() actually calls, not merely against what Receive delivered.
func assertNoLossAppendTurns(t *testing.T, chat *fakeChatService, threadID string, texts []string) {
	t.Helper()
	if n := chat.callCount(); n != len(texts) {
		t.Fatalf("AppendTurn called %d times, want exactly %d: %+v", n, len(texts), chat.calls)
	}
	seen := make(map[string]bool, len(texts))
	for _, call := range chat.calls {
		if call.threadID != threadID {
			t.Fatalf("AppendTurn threadID = %q, want %q", call.threadID, threadID)
		}
		seen[call.text] = true
	}
	for _, want := range texts {
		if !seen[want] {
			t.Fatalf("AppendTurn was never called with text %q: got %+v", want, chat.calls)
		}
	}
}

// noLossEnvelope marshals len(texts) distinct Updates, all on chatID, into
// one {"ok":true,"result":[...]}  getUpdates envelope — real JSON, decoded
// by the real client.Poll/fetchOne/decodeEnvelope chain, not hand-built
// Update structs handed straight to dispatch.
func noLossEnvelope(t *testing.T, chatID int64, texts []string) []byte {
	t.Helper()
	updates := make([]Update, len(texts))
	for i, text := range texts {
		updates[i] = textUpdate(int64(2001+i), chatID, chatID, text)
	}
	envelope := struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}{OK: true, Result: updates}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal no-loss envelope: %v", err)
	}
	return raw
}
