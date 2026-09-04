// Purpose: the redemption contract — that an approval is spendable exactly
// once, only on the action it was issued for, only while its window holds,
// and only while the grant underneath it is still in force.
//
// SPORT: internal/policy ConsumeRequest/ADDED, ConsumeResult/ADDED
// (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// redeem builds the redemption a caller sends for an entry queued by
// askRequest(action).
func redeem(res EnqueueResult, action string) ConsumeRequest {
	return ConsumeRequest{
		RequestID: res.RequestID,
		Nonce:     res.Token.Nonce,
		Action:    action,
		Params:    askRequest(action).Params,
	}
}

// approvedEntry enqueues action and records the matching approval.
func approvedEntry(t *testing.T, f *approvalFixture, action string) EnqueueResult {
	t.Helper()
	res := f.enqueue(t, action)
	if out := decideOne(t, f.queue, approve(res, L2)); out.Err != nil {
		t.Fatalf("approving %q: %v", action, out.Err)
	}
	return res
}

// TestConsumeTokenHappyPath proves a redeemed approval returns the subject,
// capability and rung it was approved under.
func TestConsumeTokenHappyPath(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := approvedEntry(t, f, "edit-a")

	out, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a"))
	if err != nil {
		t.Fatalf("ConsumeToken: %v", err)
	}
	if out.RequestID != res.RequestID || out.Capability != approvalCap().Name || out.Level != L2 {
		t.Fatalf("ConsumeResult = %+v, want the queued request at %s on %s", out, L2, approvalCap().Name)
	}
	if out.Subject != testSubject() {
		t.Errorf("ConsumeResult subject = %v, want %v", out.Subject, testSubject())
	}
	if !out.ConsumedAt.Equal(baseTime) {
		t.Errorf("ConsumedAt = %s, want the injected clock's %s", out.ConsumedAt, baseTime)
	}
	pending, err := f.queue.GetPending(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("GetPending after redemption = %v, %v; a spent approval is not pending", pending, err)
	}
}

// TestConsumeTokenRefusesAMutatedRequest is the single most important test
// in this ticket: an approval issued for one action must not carry a
// different one, whether the ACTION text changed or only its PARAMETERS.
func TestConsumeTokenRefusesAMutatedRequest(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := approvedEntry(t, f, "edit-a")

	mutatedAction := redeem(res, "edit-a")
	mutatedAction.Action = "edit-b"
	if _, err := f.queue.ConsumeToken(ctx, mutatedAction); !errors.Is(err, ErrApprovalMismatch) {
		t.Fatalf("redeeming a mutated action = %v, want ErrApprovalMismatch", err)
	}

	mutatedParams := redeem(res, "edit-a")
	mutatedParams.Params = []byte(`{"path":"/etc/passwd"}`)
	if _, err := f.queue.ConsumeToken(ctx, mutatedParams); !errors.Is(err, ErrApprovalMismatch) {
		t.Fatalf("redeeming mutated parameters = %v, want ErrApprovalMismatch", err)
	}

	// A refused mutation must not have spent the approval: the honest
	// redemption still works afterwards.
	if _, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a")); err != nil {
		t.Fatalf("the unmutated redemption after two refusals: %v", err)
	}
}

// TestConsumeTokenRefusesAForeignNonce proves the approval is bound to its
// own token, so a nonce minted for a neighbouring entry cannot redeem it.
func TestConsumeTokenRefusesAForeignNonce(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	mine := approvedEntry(t, f, "edit-a")
	theirs := approvedEntry(t, f, "edit-b")

	swapped := redeem(mine, "edit-a")
	swapped.Nonce = theirs.Token.Nonce
	if _, err := f.queue.ConsumeToken(ctx, swapped); !errors.Is(err, ErrApprovalMismatch) {
		t.Fatalf("redeeming with a neighbour's nonce = %v, want ErrApprovalMismatch", err)
	}
	empty := redeem(mine, "edit-a")
	empty.Nonce = ""
	if _, err := f.queue.ConsumeToken(ctx, empty); !errors.Is(err, ErrApprovalMismatch) {
		t.Fatalf("redeeming with no nonce = %v, want ErrApprovalMismatch", err)
	}
}

// TestConsumeTokenRefusesUnapprovedAndUnknown proves redemption never
// defaults to approved: a pending entry and an unknown id both deny.
func TestConsumeTokenRefusesUnapprovedAndUnknown(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := f.enqueue(t, "edit-a")

	if _, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a")); !errors.Is(err, ErrApprovalNotApproved) {
		t.Fatalf("redeeming a pending entry = %v, want ErrApprovalNotApproved", err)
	}
	unknown := redeem(res, "edit-a")
	unknown.RequestID = "no-such-request"
	if _, err := f.queue.ConsumeToken(ctx, unknown); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("redeeming an unknown id = %v, want ErrUnknownRequest", err)
	}
}

// TestConsumeTokenRefusesAfterExpiry proves an approval does not outlive
// its window even once it has been granted.
func TestConsumeTokenRefusesAfterExpiry(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := approvedEntry(t, f, "edit-a")

	f.clock.Advance(MaxApprovalTTL + time.Second)
	if _, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a")); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("redeeming a lapsed approval = %v, want ErrTokenExpired", err)
	}
}

// TestConsumeTokenRefusesAfterGrantRevocation proves an approval does not
// outlive its SCOPE: revoking the standing grant the entry was admitted
// under invalidates an approval that has already been granted. This is why
// the queue keeps no grant cache.
func TestConsumeTokenRefusesAfterGrantRevocation(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	if err := f.grants.Grant(ctx, Grant{
		Subject:    testSubject(),
		Capability: approvalCap().Name,
		ScopeClass: corpus.VisibilityTeam,
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	res := approvedEntry(t, f, "edit-a")

	if err := f.grants.Revoke(ctx, testSubject(), approvalCap().Name); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a"))
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("redeeming after the grant was revoked = %v, want a grant-denied refusal", err)
	}
}

// TestConsumeTokenRefusesAfterCapabilityRemoval proves the capability is
// re-resolved at redemption, so de-registering it invalidates approvals
// that were granted while it existed.
func TestConsumeTokenRefusesAfterCapabilityRemoval(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := approvedEntry(t, f, "edit-a")

	if err := f.reg.Remove(ctx, approvalCap().Name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a")); !errors.Is(err, ErrCapabilityNotFound) {
		t.Fatalf("redeeming a de-registered capability = %v, want capability-not-found", err)
	}
}

// TestConsumeTokenIsSingleUse proves a second redemption of the same
// approval is refused as a replay.
func TestConsumeTokenIsSingleUse(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := approvedEntry(t, f, "edit-a")

	if _, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a")); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if _, err := f.queue.ConsumeToken(ctx, redeem(res, "edit-a")); !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("second redemption = %v, want ErrTokenReplayed", err)
	}
}

// TestConsumeTokenRefusesWhenTheStoreIsUnreadable proves an unreachable
// ledger DENIES the redemption rather than allowing it. A queue that
// cannot record a spent nonce cannot promise single use.
func TestConsumeTokenRefusesWhenTheStoreIsUnreadable(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := approvedEntry(t, f, "edit-a")

	// Swap the ledger's store for one that cannot be reached. The entry is
	// approved, unexpired, unmutated and grant-clean; only the durable
	// claim fails, and that alone must deny.
	broken, err := NewLedger(unavailableStore{}, f.clock)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	f.queue.ledger = broken
	_, err = f.queue.ConsumeToken(ctx, redeem(res, "edit-a"))
	if !errors.Is(err, cascade.ErrUnavailable) {
		t.Fatalf("redeeming against an unreachable ledger = %v, want an unavailable refusal", err)
	}
}

// unavailableStore is a provider.Store that cannot be reached.
type unavailableStore struct{}

func (unavailableStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "store unreachable")
}
func (unavailableStore) Put(context.Context, string, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "store unreachable")
}
func (unavailableStore) Delete(context.Context, string, string) error {
	return cascade.New(cascade.KindUnavailable, "store unreachable")
}
func (unavailableStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "store unreachable")
}
func (unavailableStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return cascade.New(cascade.KindUnavailable, "store unreachable")
}

var _ provider.Store = unavailableStore{}
