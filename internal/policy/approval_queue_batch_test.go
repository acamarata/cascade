// Purpose: the batching and deduplication mechanics — that actions inside
// the window coalesce into one batch, that the batch flushes at the
// configured cap and when the window elapses, that a duplicate returns the
// existing request id without minting a second token, and that the queue
// refuses to grow past its ceiling.
//
// SPORT: internal/policy StoreApprovals/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
)

// --- batching -------------------------------------------------------------

// TestApprovalQueueBatch is the seeded-clock golden test for grouping:
// actions inside the window share a batch, the batch flushes at the cap,
// and a later action opens a new batch.
func TestApprovalQueueBatch(t *testing.T) {
	f := newApprovalFixture(t)

	a := f.enqueue(t, "edit-a")
	f.clock.Advance(2 * time.Second)
	b := f.enqueue(t, "edit-b")
	if a.BatchID != b.BatchID {
		t.Fatalf("batch ids %q and %q differ; two actions inside the window must coalesce", a.BatchID, b.BatchID)
	}
	if a.Flushed || b.Flushed {
		t.Error("a batch below the cap and inside its window must not report flushed")
	}

	// The third member reaches the cap of three, which flushes.
	c := f.enqueue(t, "edit-c")
	if c.BatchID != a.BatchID {
		t.Errorf("the capping member joined %q, want %q", c.BatchID, a.BatchID)
	}
	if !c.Flushed {
		t.Error("reaching the cap must flush the batch")
	}

	// A fourth action opens a fresh batch: the previous one is closed.
	d := f.enqueue(t, "edit-d")
	if d.BatchID == a.BatchID {
		t.Errorf("the fourth action rejoined the flushed batch %q", d.BatchID)
	}

	// And so does an action arriving after the window elapses.
	f.clock.Advance(11 * time.Second)
	e := f.enqueue(t, "edit-e")
	if e.BatchID == d.BatchID {
		t.Errorf("an action after the window rejoined batch %q", e.BatchID)
	}
	if !e.Flushed {
		t.Error("an elapsed window must flush the batch it closed")
	}
}

// TestApprovalQueueBatchCapIsConfigured proves the cap comes from the
// R-14.29 config numerics rather than from a constant in the queue.
func TestApprovalQueueBatchCapIsConfigured(t *testing.T) {
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{
		Batching: ApprovalBatching{WindowSeconds: 30, Cap: 2},
	})
	first := f.enqueue(t, "edit-a")
	second := f.enqueue(t, "edit-b")
	if !second.Flushed {
		t.Error("a cap of two must flush on the second member")
	}
	if first.BatchID != second.BatchID {
		t.Error("both members of a two-deep batch must share its id")
	}
}

// TestApprovalQueueFullRefuses proves the pending ceiling is enforced and
// that the refusal is the queue-full one rather than a silent drop.
func TestApprovalQueueFullRefuses(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{
		Batching: ApprovalBatching{WindowSeconds: 3600, Cap: 1},
	})
	ceiling := f.queue.pendingCeiling()
	for i := 0; i < ceiling; i++ {
		f.enqueue(t, fmt.Sprintf("edit-%d", i))
	}
	_, err := f.queue.Enqueue(ctx, askRequest("one-too-many"))
	if !errors.Is(err, ErrApprovalQueueFull) {
		t.Fatalf("Enqueue past the ceiling = %v, want ErrApprovalQueueFull", err)
	}
}

// --- deduplication --------------------------------------------------------

// TestApprovalQueueDedup proves a duplicate inside the open batch coalesces
// onto the existing request id, mints no second token, and lands an
// approval.dedup row.
func TestApprovalQueueDedup(t *testing.T) {
	f := newApprovalFixture(t)

	first := f.enqueue(t, "edit-a")
	minted := f.minter.n
	second := f.enqueue(t, "edit-a")

	if second.RequestID != first.RequestID {
		t.Fatalf("duplicate got request id %q, want the existing %q", second.RequestID, first.RequestID)
	}
	if !second.Deduplicated {
		t.Error("the duplicate did not report itself deduplicated")
	}
	if second.Token.Nonce != "" {
		t.Errorf("a deduplicated Enqueue minted a token (%q); one action must yield one redeemable approval", second.Token.Nonce)
	}
	if f.minter.n != minted {
		t.Errorf("the minter was called %d times for a duplicate, want %d", f.minter.n-minted, 0)
	}
	if got := f.sink.kinds(); len(got) != 2 || got[1] != audit.KindApprovalDedup {
		t.Errorf("audit kinds = %v, want the second to be %s", got, audit.KindApprovalDedup)
	}
}

// TestApprovalQueueDedupKeyIncludesParams proves the dedup key is
// (action_hash, params_hash) and not the action alone: the same verb with
// different parameters is a different question.
func TestApprovalQueueDedupKeyIncludesParams(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)

	first := f.enqueue(t, "edit-a")
	other := askRequest("edit-a")
	other.Params = []byte(`{"path":"b.txt"}`)
	second, err := f.queue.Enqueue(ctx, other)
	if err != nil {
		t.Fatalf("Enqueue with different params: %v", err)
	}
	if second.RequestID == first.RequestID {
		t.Fatal("two actions with different parameters coalesced; the dedup key must include the params digest")
	}
}

// TestApprovalQueueDedupIsScopedToOpenBatch proves the same action arriving
// after the batch closed is a NEW question, not a coalescing duplicate: the
// user has not been asked about it yet.
func TestApprovalQueueDedupIsScopedToOpenBatch(t *testing.T) {
	f := newApprovalFixture(t)
	first := f.enqueue(t, "edit-a")
	f.clock.Advance(11 * time.Second)
	second := f.enqueue(t, "edit-a")
	if second.RequestID == first.RequestID {
		t.Fatal("an action after the batch closed coalesced with the closed batch's entry")
	}
	if second.Deduplicated {
		t.Error("a fresh question reported itself deduplicated")
	}
}

// --- expiry ---------------------------------------------------------------

// TestApprovalQueueExpiry proves an entry past its exp is pruned at
// GetPending, refused with ErrTokenExpired, and NOT re-asked.
func TestApprovalQueueExpiry(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := f.enqueue(t, "edit-a")

	if got := res.Token.Expires.Sub(baseTime); got != MaxApprovalTTL {
		t.Fatalf("exp is %s after issue, want the §5.24 ceiling of %s", got, MaxApprovalTTL)
	}

	f.clock.Advance(MaxApprovalTTL + time.Second)
	pending, err := f.queue.GetPending(ctx)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("GetPending returned %d entries after expiry, want none", len(pending))
	}
	outcomes, err := f.queue.Decide(ctx, []DecisionRequest{{
		RequestID: res.RequestID, Approved: true,
		PresentedSummary: res.Summary, PresentedLevel: L2,
	}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !errors.Is(outcomes[0].Err, ErrTokenExpired) {
		t.Fatalf("deciding an expired entry = %v, want ErrTokenExpired", outcomes[0].Err)
	}
	// The queue does not re-ask: nothing new appeared.
	after, err := f.queue.GetPending(ctx)
	if err != nil || len(after) != 0 {
		t.Fatalf("GetPending after an expiry refusal = %v, %v; the queue must not re-ask", after, err)
	}
	if !hasKind(f.sink.kinds(), audit.KindApprovalExpire) {
		t.Errorf("audit kinds = %v, want an %s row", f.sink.kinds(), audit.KindApprovalExpire)
	}
}

// TestApprovalQueueExpirySweepIsRateLimited proves the background sweep
// runs at most once a minute and that it reports what it retired.
func TestApprovalQueueExpirySweepIsRateLimited(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.enqueue(t, "edit-a")

	n, err := f.queue.Expire(ctx)
	if err != nil || n != 0 {
		t.Fatalf("first sweep = %d, %v; want 0 retired and no error", n, err)
	}
	f.clock.Advance(MaxApprovalTTL + time.Second)
	// Still inside the same minute as the first sweep? No: the clock moved
	// five minutes, so the rate limit has lapsed and the sweep runs.
	if n, err = f.queue.Expire(ctx); err != nil || n != 1 {
		t.Fatalf("sweep after expiry = %d, %v; want 1 retired", n, err)
	}
	// A second sweep inside the same minute is a no-op.
	f.clock.Advance(time.Second)
	if n, err = f.queue.Expire(ctx); err != nil || n != 0 {
		t.Fatalf("sweep inside the rate-limit window = %d, %v; want 0", n, err)
	}
}

// hasKind reports whether kinds contains want.
func hasKind(kinds []audit.Kind, want audit.Kind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}
