// Purpose: fixture-decode coverage and FuzzParseCallbackQuery — split from
//   approval_test.go under Art.10.3's 300-line cap. Shares
//   approval_gates_test.go's parseCallbackQuery helper (same package).
//
// SPORT: plugins/cascade-pa/telegram FuzzParseCallbackQuery/TEST (P1-E23-W5-S48-T4).

package telegram

import (
	"context"
	"errors"
	"os"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestParseCallbackQuery_RealShapeDecodes and
// TestParseCallbackQuery_AdversarialInputRefuses cover the decode-and-validate
// path directly (the FuzzParseCallbackQuery target's non-fuzz twin).
func TestParseCallbackQuery_RealShapeDecodes(t *testing.T) {
	raw := []byte(`{"id":"cbq-1","from":{"id":111,"is_bot":false,"first_name":"Cascade"},
		"message":{"message_id":777,"chat":{"id":555,"type":"private"},"date":1},
		"data":"req-1|nonce-1"}`)
	cq, err := parseCallbackQuery(raw)
	if err != nil {
		t.Fatalf("parseCallbackQuery: %v", err)
	}
	if cq.ID != "cbq-1" || cq.From.ID != 111 || cq.Data != "req-1|nonce-1" {
		t.Errorf("decoded = %+v, unexpected shape", cq)
	}
}

func TestParseCallbackQuery_AdversarialInputRefuses(t *testing.T) {
	for _, raw := range [][]byte{
		nil, []byte(""), []byte("{"), []byte("null"), []byte(`{"id":""}`),
		[]byte(`{"id":"x","from":{"id":0}}`), []byte(`not json at all`),
	} {
		if _, err := parseCallbackQuery(raw); err == nil {
			t.Errorf("parseCallbackQuery(%q) = nil error, want a refusal", raw)
		}
	}
}

// TestApprovalDenyErrorRejected covers redeemApproval's deny-failure branch.
func TestApprovalDenyErrorRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-13", "req-13", "111", false)
	f.approvals.denyErr = cascade.New(cascade.KindUnavailable, "policy: no approval queue is wired")
	cq := testCallback("req-13|nonce-13", 111, "private")

	err := dispatch(ctx, f.deps(), cq)
	if err == nil {
		t.Fatal("deny error = nil, want a refusal")
	}
	if got := f.answers.last(); got != replyApprovalRefused {
		t.Errorf("reply = %q, want %q", got, replyApprovalRefused)
	}
}

// TestAnswer_UsesTheGuardedPath proves the exported Answer wrapper really
// calls the module's own guarded answer (refuse.go), not a second, ungated
// path — using a real TelegramModule from the rig.
func TestAnswer_UsesTheGuardedPath(t *testing.T) {
	rig := newDefaultRig(t)
	rig.module.Answer(context.Background(), "cbq-x", "private", "approved")
	methods := rig.doer.methods()
	if len(methods) != 1 || methods[0] != MethodAnswerCallbackQuery {
		t.Errorf("transport calls = %v, want exactly one %q", methods, MethodAnswerCallbackQuery)
	}
}

// TestAuthorizeApprovalSender_NilBindingRefuses and
// TestAuthorizeApprovalSender_StoreErrorRefuses cover STEP 0's own
// fail-closed branches directly.
func TestAuthorizeApprovalSender_NilBindingRefuses(t *testing.T) {
	if authorizeApprovalSender(context.Background(), nil, testSubject, "111") {
		t.Error("a nil binding store must refuse, never admit")
	}
}

// TestAuthorizeApprovalSender_StoreErrorRefuses proves an unreadable
// binding store answers false, not true: authorizeApprovalSender's own doc
// comment promises this branch, and no test previously drove it (rework
// fix 8).
func TestAuthorizeApprovalSender_StoreErrorRefuses(t *testing.T) {
	state := newMemState()
	state.loadErr = errors.New("boom: binding store unreachable")
	binding := cascadepa.NewBindingStore(fixedTestClock{t0()}, &okRegistrar{}, state)
	if authorizeApprovalSender(context.Background(), binding, testSubject, "111") {
		t.Error("an unreadable binding store must refuse, never admit")
	}
}

// TestEmitApprovalUnauthorized_UnwiredIsANoop proves a missing sink or
// clock does not panic — STEP 0's rejection already happened by the time
// this runs (mirrors publishQuarantine's identical property). The
// assertion is the recover itself (rework fix 8): a silent pass here
// previously proved nothing but the absence of a crash the runtime would
// have reported anyway.
func TestEmitApprovalUnauthorized_UnwiredIsANoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("emitApprovalUnauthorized panicked on an unwired sink/clock: %v", r)
		}
	}()
	emitApprovalUnauthorized(context.Background(), nil, fixedTestClock{t0()}, testSubject, "111", "private")
	emitApprovalUnauthorized(context.Background(), &fakeApprovalEventSink{}, nil, testSubject, "111", "private")
}

// TestLiveChatMessage_NilMessageReadsEmpty covers the bare-callback case
// (no original message reachable, e.g. Bot API's inaccessible_message).
func TestLiveChatMessage_NilMessageReadsEmpty(t *testing.T) {
	chatID, messageID := liveChatMessage(&CallbackQuery{ID: "cbq-bare"})
	if chatID != "" || messageID != "" {
		t.Errorf("liveChatMessage(no message) = (%q, %q), want empty strings", chatID, messageID)
	}
}

// TestMapGrantRefusal_NilErrorIsGeneric covers mapGrantRefusal's guard for
// a nil error (defensive: redeemApproval never calls it with one).
func TestMapGrantRefusal_NilErrorIsGeneric(t *testing.T) {
	if got := mapGrantRefusal(nil); got != replyApprovalRefused {
		t.Errorf("mapGrantRefusal(nil) = %q, want %q", got, replyApprovalRefused)
	}
}

// TestCallbackQueryFixturesDecode proves the two committed real-shaped
// fixtures decode through the same decode-and-validate path
// FuzzParseCallbackQuery drives, AND this module's own callback_data
// parser (Art.2 real-counterpart). The wire is "<request_id>|<nonce>"
// (T0 D1) — no verdict word travels, so this checks the two fields the
// wire actually carries.
func TestCallbackQueryFixturesDecode(t *testing.T) {
	for _, tc := range []struct {
		path                     string
		wantRequestID, wantNonce string
	}{
		{"testdata/callback_query_approve.json", "01J8ZC5W2K4F6H8M0P2R4T6V8X", "01J8ZC5W2K4F6H8M0P2R4T6V9Y"},
		{"testdata/callback_query_reject.json", "01J8ZC5W2K4F6H8M0P2R4T6W0Z", "01J8ZC5W2K4F6H8M0P2R4T6W1A"},
	} {
		raw, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", tc.path, err)
		}
		cq, err := parseCallbackQuery(raw)
		if err != nil {
			t.Fatalf("parseCallbackQuery(%s): %v", tc.path, err)
		}
		claim, err := parseApprovalData(cq.Data)
		if err != nil {
			t.Fatalf("parseApprovalData(%s): %v", tc.path, err)
		}
		if claim.requestID != tc.wantRequestID || claim.nonce != tc.wantNonce {
			t.Errorf("%s: claim = %+v, want requestID=%s nonce=%s", tc.path, claim, tc.wantRequestID, tc.wantNonce)
		}
	}
}

// FuzzParseCallbackQuery fuzzes the real Bot API callback_query decoder.
// Adversarial bytes must decode to an error, never panic — the same
// guarantee FuzzTelegramUpdate holds for the envelope decoder — AND (T0
// rework fix 9) must never let a decoded callback reach Grant or Deny
// when the nonce store it is dispatched against starts empty: an empty
// store can never legitimately produce a match, so ANY input that got a
// Grant/Deny call through would be a live forgery, not a fuzz false
// positive.
func FuzzParseCallbackQuery(f *testing.F) {
	for _, path := range []string{"testdata/callback_query_approve.json", "testdata/callback_query_reject.json"} {
		if raw, err := os.ReadFile(path); err == nil {
			f.Add(raw)
		}
	}
	f.Add([]byte(`{"id":"c1","from":{"id":1,"is_bot":false,"first_name":"x"},"data":"r|n"}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		cq, err := parseCallbackQuery(raw)
		if err != nil {
			return
		}
		// Every classifier this module's own flow reaches for, on whatever
		// the decoder produced. None of them may panic on any byte
		// sequence a real (or adversarial) callback_query could carry.
		_, _ = parseApprovalData(cq.Data)
		_ = chatKindOf(cq.Message)
		_, _ = liveChatMessage(cq)

		// Security invariant: dispatch the decoded callback through the
		// real handler over a FRESH, never-populated nonce store, and
		// assert zero Grant/Deny calls, whatever the bytes decoded to.
		rig := newDefaultRig(t)
		rig.bind(t, "111")
		approvals := &fakeApprovalService{entry: cascadepa.PendingApproval{Bridgeable: true}}
		deps := ApprovalHandlerDeps{
			Subject: testSubject, Binding: rig.stores.Binding, Callbacks: rig.stores.Callback,
			Approvals: approvals, Clock: fixedTestClock{t0()}, Answer: func(context.Context, string, string, string) {},
		}
		h := NewApprovalHandler(deps)
		_ = h(context.Background(), InboundMessage{
			Update: Update{CallbackQuery: cq}, Origin: OriginBridgeTelegram, Untrusted: true,
		})
		if len(approvals.grantCalls) != 0 || len(approvals.denyCalls) != 0 {
			t.Fatalf("an empty nonce store let a Grant/Deny call through for input %q", raw)
		}
	})
}
