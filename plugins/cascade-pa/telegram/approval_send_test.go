// Purpose: unit coverage for approval_send.go's producer leg — the pairing
//
//	gate, exactly what crosses the wire, and the 64-byte callback_data
//	ceiling. The round-trip acceptance proof, the transport-failure case
//	and two sanity checks live in approval_send_roundtrip_test.go (this
//	file's own 300-line cap has no room for them too).
//
// SPORT: plugins/cascade-pa/telegram approval-send/TEST (P1-E23-W5-S48-T4,
//
//	T0 D4).
package telegram

import (
	"context"
	"sync"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// roundTripSenderID/roundTripChatID is one Telegram private chat: the chat
// id and the sender id are the same number (Telegram's own private-chat
// convention, which sendOne relies on), matching
// testdata/sendmessage_approval_buttons.json's chat.id.
const roundTripSenderID = "555000111"

// sendCapturingDoer is a package-local Doer fake for this file's tests: it
// decodes the fixture response into `out` on every call (fakeDoer,
// helpers_test.go, only ever does that for MethodGetUpdates), and records
// every call so a test can assert on the exact reply_markup a send built.
type sendCapturingDoer struct {
	mu    sync.Mutex
	calls []recordedCall
	reply []byte
	err   error
}

func (d *sendCapturingDoer) Do(_ context.Context, method string, params, out any) error {
	d.mu.Lock()
	d.calls = append(d.calls, recordedCall{method: method, params: params})
	d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	if out == nil || len(d.reply) == 0 {
		return nil
	}
	return decodeEnvelope(d.reply, out)
}

func (d *sendCapturingDoer) sendParams(t *testing.T) sendApprovalButtonsParams {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if c.method == MethodSendMessage {
			if p, ok := c.params.(sendApprovalButtonsParams); ok {
				return p
			}
		}
	}
	t.Fatal("no sendMessage call was recorded")
	return sendApprovalButtonsParams{}
}

// newSenderFixture builds one TelegramApprovalSender over the fixture's
// canned sendMessage response, plus the raw doer/state for assertions.
func newSenderFixture(t *testing.T, bound bool) (*TelegramApprovalSender, *sendCapturingDoer, *memBridgeState) {
	t.Helper()
	state := newMemState()
	if bound {
		if err := state.Save(context.Background(), cascadepa.SubjectState{
			Subject: testSubject, TrustTier: "paired-device",
			AllowedFrom: []string{roundTripSenderID},
		}); err != nil {
			t.Fatalf("Save bound state: %v", err)
		}
	}
	doer := &sendCapturingDoer{reply: mustReadTestdata(t, "sendmessage_approval_buttons.json")}
	client := NewBotClient(testSubject, doer, &tierGate{}, cascadepa.NewUpdateLedger(state))
	sender := NewTelegramApprovalSender(TelegramApprovalSenderDeps{
		Subject: testSubject, State: state, Callbacks: cascadepa.NewCallbackNonceStore(),
		Client: client, Clock: fixedTestClock{t0()},
	})
	return sender, doer, state
}

// TestTelegramApprovalSender_UnpairedSubject_NothingSent proves the pairing
// gate: an unpaired subject gets no message sent and Send reports no error.
func TestTelegramApprovalSender_UnpairedSubject_NothingSent(t *testing.T) {
	sender, doer, _ := newSenderFixture(t, false)
	if err := sender.Send(context.Background(), "req-unpaired"); err != nil {
		t.Fatalf("Send: %v, want nil (nothing to notify)", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("doer.calls = %d, want 0 — an unpaired subject must send nothing", len(doer.calls))
	}
}

// TestTelegramApprovalSender_UnknownSubject_NothingSent is the "never
// bound at all" twin of the above — a subject with no row whatsoever.
func TestTelegramApprovalSender_UnknownSubject_NothingSent(t *testing.T) {
	state := newMemState()
	doer := &sendCapturingDoer{reply: mustReadTestdata(t, "sendmessage_approval_buttons.json")}
	client := NewBotClient(testSubject, doer, &tierGate{}, cascadepa.NewUpdateLedger(state))
	sender := NewTelegramApprovalSender(TelegramApprovalSenderDeps{
		Subject: "tg-never-seen", State: state, Callbacks: cascadepa.NewCallbackNonceStore(),
		Client: client, Clock: fixedTestClock{t0()},
	})
	if err := sender.Send(context.Background(), "req-unknown"); err != nil {
		t.Fatalf("Send: %v, want nil", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("doer.calls = %d, want 0", len(doer.calls))
	}
}

// TestTelegramApprovalSender_SendsButtonsWithinLimit proves what crosses
// the wire for a real 32-character (hex, queue-minted) request id: exactly
// one sendMessage call, two inline buttons, each callback_data shaped
// "<request_id>|<nonce>" — 32 hex chars + 1 separator + a 26-char
// (cascade.ID-shaped, mintNonce/cascade.NewID) nonce = 59 bytes, within
// Telegram's 64-byte callback_data ceiling.
func TestTelegramApprovalSender_SendsButtonsWithinLimit(t *testing.T) {
	sender, doer, _ := newSenderFixture(t, true)
	// 32 lower-case hex characters: the REAL shape of a queue-minted
	// request id (internal/policy/approval_queue_enqueue.go's randomID,
	// 16 bytes of hex) — not internal/policy's separate cascade.ID
	// (26-char Crockford base32), a different id space this package
	// never sees (bridge_leg.go's own "QUEUE-ID VS RECORD-ID" comment).
	const requestID = "3f9a1c2d4e5b6a7c8d9e0f1a2b3c4d5e"
	if len(requestID) != 32 {
		t.Fatalf("test fixture bug: requestID is %d chars, want 32", len(requestID))
	}
	if err := sender.Send(context.Background(), requestID); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sendCalls := 0
	for _, c := range doer.calls {
		if c.method == MethodSendMessage {
			sendCalls++
		}
	}
	if sendCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want exactly 1", sendCalls)
	}
	params := doer.sendParams(t)
	if params.ChatID != 555000111 {
		t.Errorf("ChatID = %d, want 555000111", params.ChatID)
	}
	buttons := params.ReplyMarkup.InlineKeyboard
	if len(buttons) != 1 || len(buttons[0]) != 2 {
		t.Fatalf("keyboard shape = %+v, want one row of two buttons", buttons)
	}
	for _, b := range buttons[0] {
		if len(b.CallbackData) > maxCallbackDataBytes {
			t.Errorf("callback_data %q is %d bytes, over the %d-byte ceiling",
				b.CallbackData, len(b.CallbackData), maxCallbackDataBytes)
		}
		claim, err := parseApprovalData(b.CallbackData)
		if err != nil {
			t.Fatalf("the button's own callback_data does not parse as %q|%q: %v",
				"request_id", "nonce", err)
		}
		if claim.requestID != requestID {
			t.Errorf("claim.requestID = %q, want %q", claim.requestID, requestID)
		}
	}
	if buttons[0][0].CallbackData == buttons[0][1].CallbackData {
		t.Error("both buttons carry the SAME callback_data — each must mint its own nonce")
	}
}

// TestTelegramApprovalSender_TextNamesNothing proves the outbound text is
// the FIXED string — no summary, no request id, no expiry ever crosses on
// the send path (this file's own header, "the bridge is never an oracle").
func TestTelegramApprovalSender_TextNamesNothing(t *testing.T) {
	sender, doer, _ := newSenderFixture(t, true)
	if err := sender.Send(context.Background(), "01J8ZC5W2K4F6H8M0P2R4T6V8X"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := doer.sendParams(t).Text; got != approvalPromptText {
		t.Errorf("Text = %q, want the fixed prompt %q", got, approvalPromptText)
	}
}

// TestTelegramModule_Client_ExposesTheRealBotClient proves the accessor
// producer wiring needs (internal/plugins) hands back the SAME *BotClient
// the module dispatches with, not a copy or a stand-in.
func TestTelegramModule_Client_ExposesTheRealBotClient(t *testing.T) {
	doer := &sendCapturingDoer{}
	client := NewBotClient(testSubject, doer, &tierGate{}, cascadepa.NewUpdateLedger(newMemState()))
	module := NewTelegramModule(testSubject, client, nil, nil, nil, fixedTestClock{t0()})
	if module.Client() != client {
		t.Error("Client() did not return the exact *BotClient the module was built with")
	}
}
