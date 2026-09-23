// Purpose: continued unit coverage for the R-21.210/R-21.230 inline-button
//   approval flow — split from approval_test.go under Art.10.3's 300-line
//   cap. Shares approval_test.go's fixtures/fakes (same package).
//
// SPORT: plugins/cascade-pa/telegram approval-flow/TEST (P1-E23-W5-S48-T4).

package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestCallbackGuessedRequestIDRejected: a nonce value that was never
// minted (a guess) fails the store lookup outright.
func TestCallbackGuessedRequestIDRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	cq := testCallback("req-guess|nonce-guess", 111, "private")

	if err := dispatch(ctx, f.deps(), cq); err == nil {
		t.Fatal("guessed request_id = nil error, want a refusal")
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls = %v, want none", f.approvals.grantCalls)
	}
}

// TestApprovalForbiddenClassRejected proves the fail-closed CanBridge gate:
// the host's own Bridgeable=false rejects before ANY redemption call, and
// this module adds no classifier of its own (R-16.60c).
func TestApprovalForbiddenClassRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-8", "req-8", "111", true)
	f.approvals.entry = cascadepa.PendingApproval{RequestID: "req-8", Bridgeable: false}
	cq := testCallback("req-8|nonce-8", 111, "private")

	if err := dispatch(ctx, f.deps(), cq); err == nil {
		t.Fatal("forbidden class = nil error, want a refusal")
	}
	if got := f.answers.last(); got != replyApprovalForbidden {
		t.Errorf("reply = %q, want %q", got, replyApprovalForbidden)
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls = %v, want none — CanBridge=false must block redemption", f.approvals.grantCalls)
	}
}

// TestApprovalUnresolvableRequestIDRejected proves an unresolvable
// request id (ShowPending fails) gets the SAME fixed generic reply as a
// forged signature (R-21.230's byte-identical requirement).
func TestApprovalUnresolvableRequestIDRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-9", "req-9", "111", true)
	f.approvals.showErr = cascade.New(cascade.KindNotFound, "policy: no pending approval has request id \"req-9\"")
	cq := testCallback("req-9|nonce-9", 111, "private")

	_ = dispatch(ctx, f.deps(), cq)
	if got := f.answers.last(); got != replyApprovalRefused {
		t.Errorf("reply = %q, want %q (byte-identical to the forged-signature reply)", got, replyApprovalRefused)
	}
}

// TestCallbackForgedTokenRejected (R-21.230): approval.grant refusing a
// forged/tampered signed token maps to the ONE fixed generic reply — the
// SAME bytes an unresolvable request id gets — names no field, key, token
// state or failing check, and dispatches nothing.
func TestCallbackForgedTokenRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-10", "req-10", "111", true)
	// Shaped exactly like the real RPC round trip's reconstructed error
	// (internal/client/codec.go: method+": "+server message) for
	// grantRefusal(ErrInvalidSignature) — "policy: the approval token did
	// not verify" — with no forged-token detail, key or state named.
	f.approvals.grantErr = cascade.New(cascade.KindPermissionDenied,
		"approval.grant: policy: the approval token did not verify")
	cq := testCallback("req-10|nonce-10", 111, "private")

	err := dispatch(ctx, f.deps(), cq)
	if err == nil {
		t.Fatal("forged token = nil error, want a refusal")
	}
	if got := f.answers.last(); got != replyApprovalRefused {
		t.Errorf("reply = %q, want %q (byte-identical to the unresolvable-request-id reply)", got, replyApprovalRefused)
	}
	if len(f.approvals.grantCalls) != 1 {
		t.Fatalf("grantCalls = %v, want exactly one attempt", f.approvals.grantCalls)
	}
	for _, forbidden := range []string{"token", "signature", "key", "field"} {
		if strings.Contains(f.answers.last(), forbidden) {
			t.Errorf("reply %q names %q; R-21.230 forbids it", f.answers.last(), forbidden)
		}
	}
}

// TestApprovalGrantExpiredTokenRejected and
// TestApprovalGrantReplayedTokenRejected prove the two DISTINCT actionable
// replies R-21.230 preserves (only ErrInvalidSignature is folded generic).
//
// TestApprovalGrantExpiredTokenRejected is table-driven over the TWO real
// "expired" shapes the real server actually emits (rework fix 4 — the
// earlier mapGrantRefusal matched only the second, on a Kind conjunction
// the first shape's Kind never satisfies):
func TestApprovalGrantExpiredTokenRejected(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			"verifier-layer signature expiry (rpc_grant.go grantRefusal, KindPermissionDenied)",
			cascade.New(cascade.KindPermissionDenied,
				"approval.grant: policy: the approval token is no longer valid"),
		},
		{
			"redemption-ledger expiry (approval_queue_errors.go ErrTokenExpired, KindPolicyDenied)",
			cascade.New(cascade.KindPolicyDenied,
				"approval.grant: policy: approval-token-expired: request req-11 expired at 2026-09-21T12:00:00Z"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newApprovalFixture(t)
			f.rig.bind(t, "111")
			f.storeNonce(t, "nonce-11", "req-11", "111", true)
			f.approvals.grantErr = tc.err
			cq := testCallback("req-11|nonce-11", 111, "private")

			_ = dispatch(ctx, f.deps(), cq)
			if got := f.answers.last(); got != replyApprovalExpired {
				t.Errorf("reply = %q, want %q", got, replyApprovalExpired)
			}
		})
	}
}

func TestApprovalGrantReplayedTokenRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-12", "req-12", "111", true)
	f.approvals.grantErr = cascade.New(cascade.KindConflict,
		"approval.grant: policy: request req-12 has already been redeemed")
	cq := testCallback("req-12|nonce-12", 111, "private")

	_ = dispatch(ctx, f.deps(), cq)
	if got := f.answers.last(); got != replyApprovalReplayed {
		t.Errorf("reply = %q, want %q", got, replyApprovalReplayed)
	}
}

// TestApprovalGrantElevationRequiredRejected (FLAG-2, s48t4-confirm-
// verdict.txt Q4): an approve tap that resolves to a real request the
// daemon refuses with KindElevationRequired — today's honest production
// state, no Verifier/Attestor wired (internal/plugins's real-counterpart
// test proves the real handler does this) — gets replyApprovalForbidden, a
// distinct actionable reply, never the generic "this approval could not be
// verified", which would misdescribe a missing attestation source as a
// verification failure. Shaped exactly like the real RPC round trip's
// reconstructed error (internal/client/codec.go: method+": "+server
// message) for the real refusal internal/policy/verbs.go:166-167 emits.
func TestApprovalGrantElevationRequiredRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	f.storeNonce(t, "nonce-13", "req-13", "111", true)
	f.approvals.grantErr = cascade.New(cascade.KindElevationRequired,
		"approval.grant: policy: approval.grant is an elevated verb and no attestation source is enrolled")
	cq := testCallback("req-13|nonce-13", 111, "private")

	_ = dispatch(ctx, f.deps(), cq)
	if got := f.answers.last(); got != replyApprovalForbidden {
		t.Errorf("reply = %q, want %q", got, replyApprovalForbidden)
	}
}

// TestApprovalBareCallbackNoMessageEnvelopeRejected pins rework fix 6
// (approval.go's resolveApprovalNonce, STEP 0.4): a callback_query with no
// message envelope (Bot API >= 7.0's inaccessible_message case) refuses
// BEFORE the nonce resolves to an empty chat/message id pair that could
// match an empty-bound nonce. The confirming review's own mutation (delete
// the nil-Message guard) left this case GREEN with nothing to catch it
// (FLAG-1, s48t4-confirm-verdict.txt item 6) — this is that missing test,
// over the confirmer's exact proven input.
func TestApprovalBareCallbackNoMessageEnvelopeRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	if err := f.rig.stores.Callback.Store(cascadepa.CallbackNonce{
		Nonce: "nonce-bare", RequestID: "req-bare",
		BridgeInstance: testSubject, PairedSubjectID: "111",
		ChatID: "", MessageID: "", ExpiresAt: t0().Add(time.Hour), AllowedVerdict: true,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	cq := &CallbackQuery{
		ID: "cbq-bare", From: User{ID: 111}, Message: nil,
		Data: "req-bare|nonce-bare",
	}

	err := dispatch(ctx, f.deps(), cq)
	if err == nil {
		t.Fatal("bare callback (no message envelope) = nil error, want a refusal")
	}
	if got := f.answers.last(); got != replyApprovalRefused {
		t.Errorf("reply = %q, want %q", got, replyApprovalRefused)
	}
	if len(f.approvals.grantCalls) != 0 {
		t.Errorf("grantCalls = %v, want none — a bare callback must never redeem", f.approvals.grantCalls)
	}
}

// TestCallbackMalformedDataRejected proves a callback whose data does not
// match this module's own "<request_id>|<nonce>" shape refuses instead of
// panicking or, worse, being parsed loosely.
func TestCallbackMalformedDataRejected(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.rig.bind(t, "111")
	for _, data := range []string{"", "only-id", "req|", "|nonce", "req|nonce|extra"} {
		cq := testCallback(data, 111, "private")
		if err := dispatch(ctx, f.deps(), cq); err == nil {
			t.Errorf("data %q = nil error, want a refusal", data)
		}
	}
}

// TestParseApprovalData_LengthGate is the rework fix 3 table test: exactly
// 64 bytes (Telegram's own callback_data ceiling) is accepted, 65 is
// refused outright — bytes that long could never have arrived from a real
// Bot API tap.
func TestParseApprovalData_LengthGate(t *testing.T) {
	makeData := func(total int) string {
		idLen := (total - 1) / 2
		nonceLen := total - 1 - idLen
		return strings.Repeat("r", idLen) + "|" + strings.Repeat("n", nonceLen)
	}
	at64 := makeData(64)
	if len(at64) != 64 {
		t.Fatalf("test setup: len(at64) = %d, want 64", len(at64))
	}
	if _, err := parseApprovalData(at64); err != nil {
		t.Errorf("64 bytes (Telegram's ceiling) = %v, want it accepted", err)
	}
	at65 := makeData(65)
	if len(at65) != 65 {
		t.Fatalf("test setup: len(at65) = %d, want 65", len(at65))
	}
	if _, err := parseApprovalData(at65); err == nil {
		t.Error("65 bytes (over Telegram's ceiling) = nil error, want a refusal")
	}
}

// parseCallbackQuery is FuzzParseCallbackQuery's own decode-and-validate
// path: json.Unmarshal plus the SAME validateCallbackQuery production code
// runs (approval.go), matching FuzzTelegramUpdate's own inline-decode
// pattern (fuzz_test.go) rather than a parallel exported production Parse
// function nothing else in this tree would ever call — production itself
// never hands a handler raw per-callback bytes (see approval.go's header).
func parseCallbackQuery(raw []byte) (*CallbackQuery, error) {
	var cq CallbackQuery
	if err := json.Unmarshal(raw, &cq); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "cascade-pa/telegram: malformed callback_query")
	}
	if err := validateCallbackQuery(&cq); err != nil {
		return nil, err
	}
	return &cq, nil
}
