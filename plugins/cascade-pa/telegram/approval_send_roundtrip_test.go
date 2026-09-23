// Purpose: the acceptance proof approval_send_test.go's own 300-line cap
//
//	has no room for beside its unit tests: the round trip (mint via Send,
//	tap the fixture built from the MINTED values, prove the EXISTING
//	consumer grants/denies), the transport-failure/no-orphan-nonce case,
//	and two small sanity checks (JSON shape, TTL order of magnitude).
//
// SPORT: plugins/cascade-pa/telegram approval-send-roundtrip/TEST
//
//	(P1-E23-W5-S48-T4, T0 D4).
package telegram

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// roundTripRig bundles one mint-then-consume round trip's collaborators, so
// the acceptance test itself reads as a sequence of taps and assertions
// rather than setup (Art.10.3's 50-line function cap).
type roundTripRig struct {
	t         *testing.T
	ctx       context.Context
	callbacks *cascadepa.CallbackNonceStore
	deps      ApprovalHandlerDeps
	approvals *fakeApprovalService
	answers   *answerRecorder
}

// newRoundTripRig mints requestID's two buttons via a real Send, then wires
// the consumer side (approval.go's unmodified NewApprovalHandler) over the
// SAME durable stores the sender just wrote to — exactly like binding.go's
// real host composition would. It returns the rig plus both minted
// callback_data strings.
func newRoundTripRig(t *testing.T, requestID string) (rig roundTripRig, approveData, denyData string) {
	t.Helper()
	ctx := context.Background()
	state := newMemState()
	if err := state.Save(ctx, cascadepa.SubjectState{
		Subject: testSubject, TrustTier: "paired-device", AllowedFrom: []string{roundTripSenderID},
	}); err != nil {
		t.Fatalf("Save bound state: %v", err)
	}
	callbacks := cascadepa.NewCallbackNonceStore()
	sendDoer := &sendCapturingDoer{reply: mustReadTestdata(t, "sendmessage_approval_buttons.json")}
	client := NewBotClient(testSubject, sendDoer, &tierGate{}, cascadepa.NewUpdateLedger(state))
	sender := NewTelegramApprovalSender(TelegramApprovalSenderDeps{
		Subject: testSubject, State: state, Callbacks: callbacks, Client: client, Clock: fixedTestClock{t0()},
	})
	if err := sender.Send(ctx, requestID); err != nil {
		t.Fatalf("Send: %v", err)
	}
	params := sendDoer.sendParams(t)
	approveData = params.ReplyMarkup.InlineKeyboard[0][0].CallbackData
	denyData = params.ReplyMarkup.InlineKeyboard[0][1].CallbackData

	binding := cascadepa.NewBindingStore(fixedTestClock{t0()}, &okRegistrar{}, state)
	approvals := &fakeApprovalService{entry: cascadepa.PendingApproval{Bridgeable: true}}
	answers := &answerRecorder{}
	rig = roundTripRig{
		t: t, ctx: ctx, callbacks: callbacks, approvals: approvals, answers: answers,
		deps: ApprovalHandlerDeps{
			Subject: testSubject, Binding: binding, Callbacks: callbacks,
			Approvals: approvals, Clock: fixedTestClock{t0()}, Answer: answers.Answer,
		},
	}
	return rig, approveData, denyData
}

// tap dispatches one callback_data value against the fixture's own live
// envelope (chat 555000111, message 44 — sendmessage_approval_buttons.json;
// liveChatMessage reads these off the envelope, never off callback_data, so
// a mismatch here would refuse for the wrong reason) and returns the error
// dispatch produced.
func (r roundTripRig) tap(data string) error {
	r.t.Helper()
	fromID, err := strconv.ParseInt(roundTripSenderID, 10, 64)
	if err != nil {
		r.t.Fatalf("parse sender id: %v", err)
	}
	cq := &CallbackQuery{
		ID: "cbq-roundtrip", From: User{ID: fromID, FirstName: "Cascade"},
		Message: &Message{MessageID: 44, Chat: Chat{ID: 555000111, Type: "private"}},
		Data:    data,
	}
	return dispatch(r.ctx, r.deps, cq)
}

// TestTelegramApprovalSender_RoundTrip_ConsumerGrantsAndDenies is the
// acceptance proof: mint via Send, tap the fixture built from the MINTED
// values, and prove approval.go's EXISTING, unmodified NewApprovalHandler
// grants the approve button and denies the deny button — then proves a
// second tap on an already-consumed nonce is refused (still one-use).
func TestTelegramApprovalSender_RoundTrip_ConsumerGrantsAndDenies(t *testing.T) {
	// 32 lower-case hex characters: the real shape of a queue-minted
	// request id (internal/policy/approval_queue_enqueue.go's randomID),
	// not an arbitrary label — see approval_send_test.go's own comment.
	const requestID = "12d248f3a47bdc1b076c956c7b331676"
	rig, approveData, denyData := newRoundTripRig(t, requestID)

	if err := rig.tap(approveData); err != nil {
		t.Fatalf("dispatch(approve): %v", err)
	}
	if got := rig.answers.last(); got != replyApprovalGranted {
		t.Fatalf("approve tap reply = %q, want %q", got, replyApprovalGranted)
	}
	if len(rig.approvals.grantCalls) != 1 || rig.approvals.grantCalls[0] != requestID {
		t.Fatalf("grantCalls = %v, want [%s]", rig.approvals.grantCalls, requestID)
	}

	if err := rig.tap(denyData); err != nil {
		t.Fatalf("dispatch(deny): %v", err)
	}
	if got := rig.answers.last(); got != replyApprovalDenied {
		t.Fatalf("deny tap reply = %q, want %q", got, replyApprovalDenied)
	}
	if len(rig.approvals.denyCalls) != 1 || rig.approvals.denyCalls[0] != requestID {
		t.Fatalf("denyCalls = %v, want [%s]", rig.approvals.denyCalls, requestID)
	}

	// The approve nonce was already consumed above: a second tap on it
	// refuses (still one-use, even for a producer-minted nonce) and never
	// re-dispatches.
	if err := rig.tap(approveData); err == nil {
		t.Fatal("second tap on an already-consumed nonce: err = nil, want a refusal")
	}
	if got := rig.answers.last(); got != replyApprovalRefused {
		t.Errorf("replayed-nonce reply = %q, want %q", got, replyApprovalRefused)
	}
	if len(rig.approvals.grantCalls) != 1 {
		t.Errorf("grantCalls after replay = %v, want still exactly one call", rig.approvals.grantCalls)
	}
}

// TestTelegramApprovalSender_TransportFailure_NoNoncesStored proves a
// failed send never leaves an orphan CallbackNonce record behind: Store
// only runs after the Do call the fixture backs succeeds.
//
// FLAG-2: it must consume the REAL minted nonces, not an arbitrary
// "anything" value. A nonce that was never minted finds nothing in the
// store whether or not storePair ran early, so a probe on it can never
// catch a "store before send" regression — it is an assertion that cannot
// fail. sendCapturingDoer records the attempted call BEFORE returning its
// error (approval_send_test.go's Do), so the REAL callback_data — and the
// real nonces inside it — are recoverable even from a failed send.
func TestTelegramApprovalSender_TransportFailure_NoNoncesStored(t *testing.T) {
	const requestID = "req-transport-fail"
	state := newMemState()
	if err := state.Save(context.Background(), cascadepa.SubjectState{
		Subject: testSubject, TrustTier: "paired-device", AllowedFrom: []string{roundTripSenderID},
	}); err != nil {
		t.Fatalf("Save bound state: %v", err)
	}
	callbacks := cascadepa.NewCallbackNonceStore()
	doer := &sendCapturingDoer{err: context.DeadlineExceeded}
	client := NewBotClient(testSubject, doer, &tierGate{}, cascadepa.NewUpdateLedger(state))
	sender := NewTelegramApprovalSender(TelegramApprovalSenderDeps{
		Subject: testSubject, State: state, Callbacks: callbacks, Client: client, Clock: fixedTestClock{t0()},
	})
	if err := sender.Send(context.Background(), requestID); err == nil {
		t.Fatal("Send over a failing transport: err = nil, want the transport error")
	}

	params := doer.sendParams(t)
	approveClaim, err := parseApprovalData(params.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	if err != nil {
		t.Fatalf("parsing the attempted approve callback_data: %v", err)
	}
	denyClaim, err := parseApprovalData(params.ReplyMarkup.InlineKeyboard[0][1].CallbackData)
	if err != nil {
		t.Fatalf("parsing the attempted deny callback_data: %v", err)
	}

	// Consume on EACH real, minted nonce — with the fields a "store
	// before send" bug would use: the real bridge/subject the attempt
	// was for, and the message id a fabricated Message{MessageID: 44}
	// (over a zero-value Chat, so ChatID "0") would carry.
	for _, c := range []struct {
		name  string
		nonce string
	}{{"approve", approveClaim.nonce}, {"deny", denyClaim.nonce}} {
		if _, ok := callbacks.Consume(cascadepa.CallbackClaim{
			Nonce: c.nonce, RequestID: requestID,
			BridgeInstance: testSubject, PairedSubjectID: roundTripSenderID,
			ChatID: "0", MessageID: "44",
		}, t0()); ok {
			t.Errorf("Consume found the real %s nonce after a failed send; none should have been stored", c.name)
		}
	}
}

// TestSendApprovalButtonsParams_JSONShape proves sendApprovalButtonsParams
// round-trips through encoding/json the way the real transport
// (apicall.go's httpDoer) will marshal it, so a struct-tag typo fails here
// rather than only against a live server.
func TestSendApprovalButtonsParams_JSONShape(t *testing.T) {
	p := sendApprovalButtonsParams{
		ChatID: 42, Text: "hi",
		ReplyMarkup: inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{{
			{Text: "Approve", CallbackData: "a|b"},
			{Text: "Deny", CallbackData: "c|d"},
		}}},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := round["reply_markup"]; !ok {
		t.Errorf("marshaled JSON has no reply_markup field: %s", raw)
	}
}

// TestBridgeApprovalNonceTTL_IsMinutesNotSeconds asserts the constant's
// ORDER of magnitude directly — a small guard against a units typo
// (seconds vs minutes) in that declaration.
func TestBridgeApprovalNonceTTL_IsMinutesNotSeconds(t *testing.T) {
	if bridgeApprovalNonceTTL < time.Minute || bridgeApprovalNonceTTL > time.Hour {
		t.Errorf("bridgeApprovalNonceTTL = %s, want something between a minute and an hour", bridgeApprovalNonceTTL)
	}
}
