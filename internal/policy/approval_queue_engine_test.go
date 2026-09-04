// Purpose: the S-18 engine seam — that an ask verdict is filed with the
// approval queue and comes back carrying a request id, that an engine with
// no queue is unchanged, and that a queue refusal DOWNGRADES the outcome to
// deny rather than stranding it at ask.
//
// SPORT: internal/policy Engine/CHANGED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// askEngine builds an engine whose profile asks at L2, over the approval
// fixture's real store, and attaches q.
func askEngine(t *testing.T, f *approvalFixture, q ApprovalQueue) *Engine {
	t.Helper()
	ctrl := NewController(nil)
	profile, err := Resolve(Config{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ctrl.profile.Store(profile)
	eng, err := NewEngine(f.reg, f.grants, ctrl)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng.WithApprovalQueue(q)
}

// evalRequest is the L2 request the seam tests evaluate.
func evalRequest() EvalRequest {
	return EvalRequest{
		Subject:    testSubject(),
		Capability: approvalCap().Name,
		Level:      L2,
		Action:     "edit-a",
		Params:     []byte(`{"path":"a.txt"}`),
		Summary:    "write edit-a",
	}
}

// TestEngineFilesAnAskVerdictWithTheQueue proves the ask path reaches the
// queue and that the outcome carries the request id — and ONLY the request
// id, never the token or the nonce.
func TestEngineFilesAnAskVerdictWithTheQueue(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	eng := askEngine(t, f, f.queue)

	out, err := eng.Evaluate(ctx, evalRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictAsk {
		t.Fatalf("verdict = %s, want ask (the baseline profile asks at L2)", out.Verdict)
	}
	if out.ApprovalRequestID == "" {
		t.Fatal("an ask verdict carried no approval request id")
	}
	pending, err := f.queue.GetPending(ctx)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 1 || pending[0].RequestID != out.ApprovalRequestID {
		t.Fatalf("pending = %+v, want the one entry the engine filed (%s)", pending, out.ApprovalRequestID)
	}
	if strings.Contains(pending[0].Summary, "nonce") {
		t.Errorf("the pending summary leaks token material: %q", pending[0].Summary)
	}
}

// TestEngineWithoutAQueueIsUnchanged proves an engine with no queue
// attached still evaluates identically; it simply files nothing.
func TestEngineWithoutAQueueIsUnchanged(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	eng := askEngine(t, f, nil)

	out, err := eng.Evaluate(ctx, evalRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictAsk || out.ApprovalRequestID != "" {
		t.Fatalf("outcome = %+v, want an ask verdict with no request id", out)
	}
	if (*Engine)(nil).WithApprovalQueue(f.queue) != nil {
		t.Error("WithApprovalQueue on a nil engine returned something")
	}
}

// TestEngineDowngradesWhenTheQueueRefuses proves an action the queue will
// not carry becomes a DENY rather than staying at ask. An action nobody can
// ever approve must not be presented as merely awaiting approval.
func TestEngineDowngradesWhenTheQueueRefuses(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	eng := askEngine(t, f, f.queue)

	// A summary the queue refuses (empty) stands in for any admission
	// refusal: the engine must not leave the outcome at ask.
	req := evalRequest()
	req.Summary = ""
	req.Capability = approvalCap().Name
	blank, err := eng.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if blank.Verdict != VerdictAsk {
		t.Fatalf("an empty summary is filled in from the capability name; verdict = %s, want ask", blank.Verdict)
	}

	// An elevation-class verb is the real case.
	elevated := evalRequest()
	elevated.Verb = "vault.rotate"
	out, err := eng.Evaluate(ctx, elevated)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny {
		t.Fatalf("verdict = %s, want deny — the queue refused to carry the action", out.Verdict)
	}
	if !strings.Contains(out.Reason, "authorized locally") {
		t.Errorf("reason = %q, want it to name the local-authorization route", out.Reason)
	}
	if out.ApprovalRequestID != "" {
		t.Error("a refused action acquired an approval request id")
	}
}

// TestAskRefusalReasonNamesTheCause proves each named refusal renders words
// a user can act on, and that an unnamed one still renders the queue's own
// reason rather than nothing.
func TestAskRefusalReasonNamesTheCause(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{refuse(ErrLocalOnly, "x"), "authorized locally"},
		{refuse(ErrApprovalQueueFull, "x"), "queue is full"},
		{errors.New("something else"), "refused this action"},
	}
	for _, tc := range cases {
		if got := askRefusalReason(tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("askRefusalReason(%v) = %q, want it to contain %q", tc.err, got, tc.want)
		}
	}
	if got := approvalSummary(EvalRequest{Capability: "workspace.write"}); got != "workspace.write" {
		t.Errorf("approvalSummary with no summary = %q, want the capability name", got)
	}
	if got := approvalSummary(EvalRequest{Summary: "mine"}); got != "mine" {
		t.Errorf("approvalSummary = %q, want the caller's own summary", got)
	}
}

// TestRandomTokenMinterIsUsable proves the shipped default minter produces
// distinct, well-formed ids — the property the deterministic test minter
// deliberately does not have.
func TestRandomTokenMinterIsUsable(t *testing.T) {
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		token, err := (randomTokenMinter{}).Mint(ctx, ApprovalMintRequest{})
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if err := validateNonce(token.RequestID); err != nil {
			t.Fatalf("minted request id is not a well-formed key component: %v", err)
		}
		if err := validateNonce(token.Nonce); err != nil {
			t.Fatalf("minted nonce is not a well-formed key component: %v", err)
		}
		if seen[token.Nonce] {
			t.Fatalf("the minter repeated nonce %q", token.Nonce)
		}
		seen[token.Nonce] = true
	}
}

// TestApprovalErrorSentinelsAreDistinguishable proves the whole reason
// ApprovalError exists: several refusals share a taxonomy Kind, and a test
// (or a caller) must still be able to tell them apart.
func TestApprovalErrorSentinelsAreDistinguishable(t *testing.T) {
	expired := refuse(ErrTokenExpired, "x")
	if !errors.Is(expired, ErrTokenExpired) {
		t.Error("an expiry refusal does not match its own sentinel")
	}
	if errors.Is(expired, ErrApprovalMismatch) {
		t.Error("an expiry refusal matches the mismatch sentinel; the two share a Kind and must still differ")
	}
	if errors.Is(expired, errors.New("not an approval error")) {
		t.Error("an approval refusal matched a foreign error type")
	}
	var nilErr *ApprovalError
	if got := nilErr.Error(); got == "" {
		t.Error("a nil ApprovalError renders an empty message")
	}
	if nilErr.Unwrap() != nil {
		t.Error("a nil ApprovalError unwrapped to something")
	}
	bare := &ApprovalError{Code: CodeApprovalMismatch}
	if got := bare.Error(); got == "" {
		t.Error("an ApprovalError with no cause renders an empty message")
	}
}
