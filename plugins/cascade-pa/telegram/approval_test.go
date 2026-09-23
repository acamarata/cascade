// Purpose: unit coverage for the R-21.210/R-21.230 inline-button approval
//   flow (approval.go, approval_auth.go) — every ordered gate, both
//   verdicts, and the R-21.230 forged-token case.
//
// SPORT: plugins/cascade-pa/telegram approval-flow/TEST (P1-E23-W5-S48-T4).

package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// fakeApprovalService is cascadepa.ApprovalService's test double.
type fakeApprovalService struct {
	showErr               error
	entry                 cascadepa.PendingApproval
	grantErr, denyErr     error
	grantCalls, denyCalls []string
}

func (f *fakeApprovalService) ShowPending(_ context.Context, _ string) (cascadepa.PendingApproval, error) {
	if f.showErr != nil {
		return cascadepa.PendingApproval{}, f.showErr
	}
	return f.entry, nil
}

func (f *fakeApprovalService) Grant(_ context.Context, requestID string) error {
	f.grantCalls = append(f.grantCalls, requestID)
	return f.grantErr
}

func (f *fakeApprovalService) Deny(_ context.Context, requestID string) error {
	f.denyCalls = append(f.denyCalls, requestID)
	return f.denyErr
}

// answerRecorder captures every deps.Answer call.
type answerRecorder struct {
	calls []struct{ callbackID, chatKind, text string }
}

func (a *answerRecorder) Answer(_ context.Context, callbackID, chatKind, text string) {
	a.calls = append(a.calls, struct{ callbackID, chatKind, text string }{callbackID, chatKind, text})
}

func (a *answerRecorder) last() string {
	if len(a.calls) == 0 {
		return ""
	}
	return a.calls[len(a.calls)-1].text
}

// fakeApprovalEventSink captures every STEP 0 rejection.
type fakeApprovalEventSink struct {
	events []ApprovalUnauthorizedEvent
}

func (f *fakeApprovalEventSink) EmitApprovalUnauthorized(_ context.Context, e ApprovalUnauthorizedEvent) {
	f.events = append(f.events, e)
}

// approvalFixture bundles one rig plus the approval-specific fakes.
type approvalFixture struct {
	rig       *testRig
	approvals *fakeApprovalService
	events    *fakeApprovalEventSink
	answers   *answerRecorder
}

func newApprovalFixture(t *testing.T) *approvalFixture {
	t.Helper()
	return &approvalFixture{
		rig:       newDefaultRig(t),
		approvals: &fakeApprovalService{entry: cascadepa.PendingApproval{Bridgeable: true}},
		events:    &fakeApprovalEventSink{},
		answers:   &answerRecorder{},
	}
}

// deps builds ApprovalHandlerDeps over the fixture's fakes.
func (f *approvalFixture) deps() ApprovalHandlerDeps {
	return ApprovalHandlerDeps{
		Subject: testSubject, Binding: f.rig.stores.Binding, Callbacks: f.rig.stores.Callback,
		Approvals: f.approvals, Events: f.events, Clock: fixedTestClock{t0()}, Answer: f.answers.Answer,
	}
}

// storeNonce mints a nonce record bound exactly to the live callback this
// file's helpers build (chat 555, message 777).
func (f *approvalFixture) storeNonce(t *testing.T, nonce, requestID, subjectID string, verdict bool) {
	t.Helper()
	if err := f.rig.stores.Callback.Store(cascadepa.CallbackNonce{
		Nonce: nonce, RequestID: requestID,
		BridgeInstance: testSubject, PairedSubjectID: subjectID,
		ChatID: "555", MessageID: "777", ExpiresAt: t0().Add(time.Hour), AllowedVerdict: verdict,
	}); err != nil {
		t.Fatalf("Store nonce: %v", err)
	}
}

// testCallback builds a real-shaped CallbackQuery.
func testCallback(data string, fromID int64, chatType string) *CallbackQuery {
	return &CallbackQuery{
		ID: "cbq-1", From: User{ID: fromID, FirstName: "Cascade"},
		Message: &Message{MessageID: 777, Chat: Chat{ID: 555, Type: chatType}},
		Data:    data,
	}
}

func dispatch(ctx context.Context, deps ApprovalHandlerDeps, cq *CallbackQuery) error {
	h := NewApprovalHandler(deps)
	return h(ctx, InboundMessage{Update: Update{CallbackQuery: cq}, Origin: OriginBridgeTelegram, Untrusted: true})
}

// TestApprovalValidTapApprovesAndDispatches proves the full happy path:
// sender authorized, nonce consumed, CanBridge admits, approval.grant
// called with the request id alone, dispatched via the queue mechanics
// the fake ApprovalService stands in for.
func TestApprovalValidTapApprovesAndDispatches(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-1", "req-1", "111", true)
	cq := testCallback("req-1|nonce-1", 111, "private")

	if err := dispatch(ctx, f.deps(), cq); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := f.answers.last(); got != replyApprovalGranted {
		t.Errorf("reply = %q, want %q", got, replyApprovalGranted)
	}
	if len(f.approvals.grantCalls) != 1 || f.approvals.grantCalls[0] != "req-1" {
		t.Errorf("grantCalls = %v, want [req-1]", f.approvals.grantCalls)
	}
	if len(f.approvals.denyCalls) != 0 {
		t.Errorf("denyCalls = %v, want none on an approve tap", f.approvals.denyCalls)
	}
}

// TestApprovalValidRejectTapDenies is the verdict=false twin.
func TestApprovalValidRejectTapDenies(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-2", "req-2", "111", false)
	cq := testCallback("req-2|nonce-2", 111, "private")

	if err := dispatch(ctx, f.deps(), cq); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := f.answers.last(); got != replyApprovalDenied {
		t.Errorf("reply = %q, want %q", got, replyApprovalDenied)
	}
	if len(f.approvals.denyCalls) != 1 || f.approvals.denyCalls[0] != "req-2" {
		t.Errorf("denyCalls = %v, want [req-2]", f.approvals.denyCalls)
	}
}

// TestCallbackNonOwnerRejected: STEP 0 (R-21.210) — a tap from a Telegram
// user id never bound on this subject rejects before nonce resolution, with
// the distinct actionable reply, the typed event, and no nonce burn: the
// owner's later, correct tap on the SAME nonce still redeems it.
func TestCallbackNonOwnerRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111") // owner
	f.storeNonce(t, "nonce-3", "req-3", "111", true)
	cq := testCallback("req-3|nonce-3", 999, "private") // stranger taps

	err := dispatch(ctx, f.deps(), cq)
	if !errors.Is(err, ErrApprovalSenderUnauthorized) {
		t.Fatalf("err = %v, want ErrApprovalSenderUnauthorized", err)
	}
	if got := f.answers.last(); got != replyApprovalUnauthorized {
		t.Errorf("reply = %q, want %q", got, replyApprovalUnauthorized)
	}
	if len(f.events.events) != 1 || f.events.events[0].SenderID != "999" {
		t.Fatalf("events = %+v, want one unauthorized event for sender 999", f.events.events)
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls = %v, want none — the gate must block before redemption", f.approvals.grantCalls)
	}

	// The stranger's refused tap never touched the nonce store (it was
	// rejected at STEP 0, before nonce resolution), so the owner's later,
	// correct tap on the SAME nonce still succeeds.
	owner := testCallback("req-3|nonce-3", 111, "private")
	if err := dispatch(ctx, f.deps(), owner); err != nil {
		t.Fatalf("owner's tap after a stranger's refused tap: %v", err)
	}
	if got := f.answers.last(); got != replyApprovalGranted {
		t.Errorf("owner's tap reply = %q, want %q", got, replyApprovalGranted)
	}
	if len(f.approvals.grantCalls) != 1 || f.approvals.grantCalls[0] != "req-3" {
		t.Errorf("grantCalls = %v, want [req-3] after the owner's correct tap", f.approvals.grantCalls)
	}
}

// TestCallbackGroupChatRejected is the group-chat variant: a non-owner
// group member's tap rejects identically, with the chat kind recorded.
func TestCallbackGroupChatRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-4", "req-4", "111", true)
	cq := testCallback("req-4|nonce-4", 222, "group")

	err := dispatch(ctx, f.deps(), cq)
	if !errors.Is(err, ErrApprovalSenderUnauthorized) {
		t.Fatalf("err = %v, want ErrApprovalSenderUnauthorized", err)
	}
	if len(f.events.events) != 1 || f.events.events[0].ChatKind != "group" {
		t.Fatalf("events = %+v, want one event with chat_kind=group", f.events.events)
	}
}

// TestCallbackNonceReplayRejected: a nonce already consumed once cannot be
// consumed again (R-21.210's "a replayed nonce... fails closed").
func TestCallbackNonceReplayRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-5", "req-5", "111", true)
	cq := testCallback("req-5|nonce-5", 111, "private")

	if err := dispatch(ctx, f.deps(), cq); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	f.approvals.grantCalls = nil
	f.answers.calls = nil

	if err := dispatch(ctx, f.deps(), cq); err == nil {
		t.Fatal("second dispatch (replay) = nil error, want a refusal")
	}
	if got := f.answers.last(); got != replyApprovalRefused {
		t.Errorf("replay reply = %q, want %q", got, replyApprovalRefused)
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls on replay = %v, want none — the token must never be touched", f.approvals.grantCalls)
	}
}

// TestCallbackForwardedButtonRejected: the SAME nonce, presented from a
// different chat/message than it was bound to, fails the live-envelope
// comparison — a forwarded button is refused without anything in this
// package having to detect "forwarded" itself.
func TestCallbackForwardedButtonRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	if err := f.rig.stores.Callback.Store(cascadepa.CallbackNonce{
		Nonce: "nonce-6", RequestID: "req-6",
		BridgeInstance: testSubject, PairedSubjectID: "111",
		ChatID: "555", MessageID: "777", ExpiresAt: t0().Add(time.Hour), AllowedVerdict: true,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	cq := &CallbackQuery{
		ID: "cbq-fwd", From: User{ID: 111},
		Message: &Message{MessageID: 909, Chat: Chat{ID: 606, Type: "private"}}, // different chat/message
		Data:    "req-6|nonce-6",
	}

	err := dispatch(ctx, f.deps(), cq)
	if err == nil {
		t.Fatal("forwarded button = nil error, want a refusal")
	}
	if got := f.answers.last(); got != replyApprovalRefused {
		t.Errorf("reply = %q, want %q", got, replyApprovalRefused)
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls = %v, want none", f.approvals.grantCalls)
	}
}

// TestCallbackRepairedSubjectRejected: after a re-pairing changed WHO is
// bound, a nonce minted for the old subject id fails PairedSubjectID
// comparison even though the caller is on the CURRENT allowlist.
func TestCallbackRepairedSubjectRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111") // the subject the nonce was minted for
	f.storeNonce(t, "nonce-7", "req-7", "111", true)
	f.rig.bind(t, "222") // re-pairing admits a second/replacement sender
	cq := testCallback("req-7|nonce-7", 222, "private")

	err := dispatch(ctx, f.deps(), cq)
	if err == nil {
		t.Fatal("re-paired subject = nil error, want a refusal")
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls = %v, want none", f.approvals.grantCalls)
	}
}
