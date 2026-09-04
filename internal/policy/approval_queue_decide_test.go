// Purpose: the decision contract — that an approval binds to the exact
// thing the approver was shown, that a batch cannot launder a rung, that a
// denial is confined to the entry it was recorded against, and that a
// repeated, late or unknown decision each deny.
//
// SPORT: internal/policy DecisionRequest/ADDED, DecisionOutcome/ADDED
// (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
)

// approve builds the decision a surface sends after showing entry.
func approve(res EnqueueResult, level RiskLevel) DecisionRequest {
	return DecisionRequest{
		RequestID:        res.RequestID,
		Approved:         true,
		PresentedSummary: res.Summary,
		PresentedLevel:   level,
	}
}

// deny builds a refusal decision.
func deny(res EnqueueResult) DecisionRequest {
	return DecisionRequest{RequestID: res.RequestID}
}

// decideOne runs a single decision and returns its outcome.
func decideOne(t *testing.T, q *StoreApprovals, req DecisionRequest) DecisionOutcome {
	t.Helper()
	outcomes, err := q.Decide(context.Background(), []DecisionRequest{req})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("Decide returned %d outcomes, want 1", len(outcomes))
	}
	return outcomes[0]
}

// TestDecideBindsToWhatWasShown is the central assertion of this file: an
// approval recorded against a DIFFERENT description than the entry carries
// is refused. A surface that rendered a stale prompt cannot approve the
// current one.
func TestDecideBindsToWhatWasShown(t *testing.T) {
	f := newApprovalFixture(t)
	res := f.enqueue(t, "edit-a")

	stale := approve(res, L2)
	stale.PresentedSummary = "write something else [L2]"
	out := decideOne(t, f.queue, stale)
	if !errors.Is(out.Err, ErrApprovalMismatch) {
		t.Fatalf("approving against a different description = %v, want ErrApprovalMismatch", out.Err)
	}
	if out.State != ApprovalPending {
		t.Errorf("the entry is %s after a refused approval, want it left pending", out.State)
	}

	// The honest decision still works.
	if out := decideOne(t, f.queue, approve(res, L2)); out.Err != nil || out.State != ApprovalApproved {
		t.Fatalf("the matching approval = %v / %s, want approved", out.Err, out.State)
	}
}

// TestDecideRefusesALowerRungThanWasQueued proves an approval cannot be
// recorded at a rung below the entry's own — the batch-laundering case,
// where a prompt presented as L2 would otherwise carry an L3 member
// through on the L2 answer.
func TestDecideRefusesALowerRungThanWasQueued(t *testing.T) {
	f := newApprovalFixture(t)
	req := askRequest("push")
	req.Level = L3
	res, err := f.queue.Enqueue(context.Background(), req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	low := approve(res, L2)
	low.PresentedSummary = res.Summary
	out := decideOne(t, f.queue, low)
	if !errors.Is(out.Err, ErrApprovalRungMismatch) {
		t.Fatalf("approving an L3 entry at L2 = %v, want ErrApprovalRungMismatch", out.Err)
	}

	invalid := approve(res, RiskLevel(0))
	if out := decideOne(t, f.queue, invalid); !errors.Is(out.Err, ErrApprovalRungMismatch) {
		t.Fatalf("approving at an invalid rung = %v, want ErrApprovalRungMismatch", out.Err)
	}

	if out := decideOne(t, f.queue, approve(res, L3)); out.Err != nil {
		t.Fatalf("approving an L3 entry at L3: %v", out.Err)
	}
}

// TestDecideMixedRungBatchDoesNotLaunder is the explicit mixed-rung batch
// case. Three actions collect into one batch, two at L2 and one at L3. A
// surface that showed the batch as L2 may approve the two L2 members and
// must NOT carry the L3 member with them.
func TestDecideMixedRungBatchDoesNotLaunder(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)

	low1 := f.enqueue(t, "edit-a")
	high := askRequest("push-to-remote")
	high.Level = L3
	highRes, err := f.queue.Enqueue(ctx, high)
	if err != nil {
		t.Fatalf("Enqueue L3: %v", err)
	}
	low2 := f.enqueue(t, "edit-b")
	if low1.BatchID != highRes.BatchID || low2.BatchID != highRes.BatchID {
		t.Fatalf("the three actions did not share a batch: %q %q %q",
			low1.BatchID, highRes.BatchID, low2.BatchID)
	}
	// The rung is IN the summary, so a surface cannot show one rung and
	// mean another.
	if highRes.Summary == low1.Summary {
		t.Fatal("an L3 entry and an L2 entry rendered the same summary")
	}

	outcomes, err := f.queue.Decide(ctx, []DecisionRequest{
		approve(low1, L2), approve(highRes, L2), approve(low2, L2),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcomes[0].Err != nil || outcomes[0].State != ApprovalApproved {
		t.Errorf("the first L2 member = %v / %s, want approved", outcomes[0].Err, outcomes[0].State)
	}
	if !errors.Is(outcomes[1].Err, ErrApprovalRungMismatch) {
		t.Errorf("the L3 member = %v, want ErrApprovalRungMismatch — a batch must not raise what was shown", outcomes[1].Err)
	}
	if outcomes[1].State == ApprovalApproved {
		t.Error("the L3 member was approved on an L2 answer")
	}
	if outcomes[2].Err != nil || outcomes[2].State != ApprovalApproved {
		t.Errorf("the second L2 member = %v / %s, want approved", outcomes[2].Err, outcomes[2].State)
	}
}

// TestDecideDenialIsNotRecordedForNeighbours proves one denial inside a
// batch leaves every other member exactly where it was.
func TestDecideDenialIsNotRecordedForNeighbours(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	a := f.enqueue(t, "edit-a")
	b := f.enqueue(t, "edit-b")
	c := f.enqueue(t, "edit-c")

	outcomes, err := f.queue.Decide(ctx, []DecisionRequest{deny(b)})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcomes[0].State != ApprovalDenied {
		t.Fatalf("the denied entry is %s, want denied", outcomes[0].State)
	}
	pending, err := f.queue.GetPending(ctx)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 2 || pending[0].RequestID != a.RequestID || pending[1].RequestID != c.RequestID {
		t.Fatalf("pending after one denial = %+v, want %s and %s still awaiting a decision",
			pending, a.RequestID, c.RequestID)
	}
	// And the denied entry cannot be redeemed.
	if _, err := f.queue.ConsumeToken(ctx, ConsumeRequest{
		RequestID: b.RequestID, Nonce: b.Token.Nonce,
		Action: "edit-b", Params: askRequest("edit-b").Params,
	}); !errors.Is(err, ErrApprovalNotApproved) {
		t.Fatalf("redeeming a denied entry = %v, want ErrApprovalNotApproved", err)
	}
}

// TestDecideRefusesReplayAndUnknown covers the fail-closed decision paths:
// an unknown id, a second decision on a decided entry, and an empty batch.
func TestDecideRefusesReplayAndUnknown(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := f.enqueue(t, "edit-a")

	if out := decideOne(t, f.queue, DecisionRequest{RequestID: "no-such-request"}); !errors.Is(out.Err, ErrUnknownRequest) {
		t.Fatalf("deciding an unknown id = %v, want ErrUnknownRequest", out.Err)
	}
	if out := decideOne(t, f.queue, approve(res, L2)); out.Err != nil {
		t.Fatalf("first decision: %v", out.Err)
	}
	if out := decideOne(t, f.queue, approve(res, L2)); !errors.Is(out.Err, ErrApprovalDecided) {
		t.Fatalf("a replayed decision = %v, want ErrApprovalDecided", out.Err)
	}
	if out := decideOne(t, f.queue, deny(res)); !errors.Is(out.Err, ErrApprovalDecided) {
		t.Fatalf("reversing a decision = %v, want ErrApprovalDecided", out.Err)
	}
	if _, err := f.queue.Decide(ctx, nil); err == nil {
		t.Error("Decide with no decisions = nil error, want a refusal")
	}
}

// TestDecideRefusesAfterExpiry proves a decision arriving after the token
// expired is refused and the entry retired, whether it was pending or had
// already been approved.
func TestDecideRefusesAfterExpiry(t *testing.T) {
	f := newApprovalFixture(t)
	pendingRes := f.enqueue(t, "edit-a")
	approvedRes := f.enqueue(t, "edit-b")
	if out := decideOne(t, f.queue, approve(approvedRes, L2)); out.Err != nil {
		t.Fatalf("approving: %v", out.Err)
	}

	f.clock.Advance(MaxApprovalTTL + time.Second)

	if out := decideOne(t, f.queue, approve(pendingRes, L2)); !errors.Is(out.Err, ErrTokenExpired) {
		t.Fatalf("deciding a lapsed pending entry = %v, want ErrTokenExpired", out.Err)
	}
	if out := decideOne(t, f.queue, approve(approvedRes, L2)); !errors.Is(out.Err, ErrTokenExpired) {
		t.Fatalf("re-deciding a lapsed approved entry = %v, want ErrTokenExpired", out.Err)
	}
}

// TestCancelInvalidatesAnApproval proves a withdrawn entry is terminal:
// it leaves the pending list, refuses redemption, and cannot be decided
// or cancelled twice.
func TestCancelInvalidatesAnApproval(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := f.enqueue(t, "edit-a")
	if out := decideOne(t, f.queue, approve(res, L2)); out.Err != nil {
		t.Fatalf("approving: %v", out.Err)
	}

	if err := f.queue.Cancel(ctx, res.RequestID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := f.queue.ConsumeToken(ctx, ConsumeRequest{
		RequestID: res.RequestID, Nonce: res.Token.Nonce,
		Action: "edit-a", Params: askRequest("edit-a").Params,
	}); !errors.Is(err, ErrApprovalCanceled) {
		t.Fatalf("redeeming a cancelled approval = %v, want ErrApprovalCanceled", err)
	}
	if err := f.queue.Cancel(ctx, res.RequestID); !errors.Is(err, ErrApprovalCanceled) {
		t.Fatalf("cancelling twice = %v, want ErrApprovalCanceled", err)
	}
	if err := f.queue.Cancel(ctx, "no-such-request"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("cancelling an unknown id = %v, want ErrUnknownRequest", err)
	}
	if !hasKind(f.sink.kinds(), audit.KindApprovalDeny) {
		t.Errorf("audit kinds = %v, want an %s row for the cancellation", f.sink.kinds(), audit.KindApprovalDeny)
	}
}

// TestApprovalStateStringIsTotal proves every state renders a stable name
// and that an out-of-range value renders as invalid rather than as any
// real state.
func TestApprovalStateStringIsTotal(t *testing.T) {
	want := map[ApprovalState]string{
		ApprovalPending:  "pending",
		ApprovalApproved: "approved",
		ApprovalDenied:   "denied",
		ApprovalConsumed: "consumed",
		ApprovalExpired:  "expired",
		ApprovalCanceled: "canceled",
	}
	for _, state := range []ApprovalState{
		ApprovalPending, ApprovalApproved, ApprovalDenied,
		ApprovalConsumed, ApprovalExpired, ApprovalCanceled,
	} {
		if got := state.String(); got != want[state] {
			t.Errorf("state %d renders %q, want %q", state, got, want[state])
		}
	}
	if got := ApprovalState(0).String(); got != "invalid-approval-state" {
		t.Errorf("the zero state renders %q, want invalid-approval-state", got)
	}
	if got := ApprovalState(99).String(); got != "invalid-approval-state" {
		t.Errorf("an out-of-range state renders %q, want invalid-approval-state", got)
	}
	if err := terminalRefusal(ApprovalPending, "r"); err != nil {
		t.Errorf("terminalRefusal on a pending entry = %v, want nil", err)
	}
	if err := terminalRefusal(ApprovalState(99), "r"); !errors.Is(err, ErrApprovalNotApproved) {
		t.Errorf("terminalRefusal on an invalid state = %v, want ErrApprovalNotApproved", err)
	}
	if got := stateOf(nil); got != ApprovalPending {
		t.Errorf("stateOf(nil) = %s, want pending", got)
	}
}
