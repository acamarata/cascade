package policy

// Purpose: the ticket's central assertion — a dry run changes NOTHING.
//   The audit domain, the policy domain (grants and the single-use
//   ledger) and the approval queue's pending set are captured before and
//   after a simulation of every verdict and compared exactly; and a
//   simulation discloses nothing about a subject that is not the one being
//   simulated.
// Constraints: the comparison is taken through the REAL B-layer store
//   (providers/sqlite on a t.TempDir file, Art.2/Art.7.1), by scanning the
//   domains rather than by asking the components whether they think they
//   wrote — a component that wrote by accident would answer no.
// SPORT: internal/policy DryRunResult/ADDED (P1-E09-W2-S18-T4).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/storage"
)

// storeState is a content digest of every row in the domains a policy
// decision can write to, plus the row count, so a failure can say whether
// rows appeared or changed.
type storeState struct {
	rows   int
	digest string
}

// snapshot digests the audit and policy domains. The audit domain holds
// both the audit log's records and the approval ledger's spent nonces; the
// policy domain holds the grants. Keys are sorted before hashing, so the
// digest depends on content alone and not on the store's iteration order.
func (f *dryRunFixture) snapshot(t *testing.T) storeState {
	t.Helper()
	ctx := context.Background()
	var lines []string
	for _, ns := range []string{string(storage.DomainAudit), string(storage.DomainPolicy)} {
		it, err := f.db.Scan(ctx, ns, "")
		if err != nil {
			t.Fatalf("scanning the %s domain: %v", ns, err)
		}
		for it.Next(ctx) {
			sum := sha256.Sum256(it.Value())
			lines = append(lines, ns+"\x00"+it.Key()+"\x00"+hex.EncodeToString(sum[:]))
		}
		if err := it.Err(); err != nil {
			_ = it.Close()
			t.Fatalf("iterating the %s domain: %v", ns, err)
		}
		if err := it.Close(); err != nil {
			t.Fatalf("closing the %s iterator: %v", ns, err)
		}
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return storeState{rows: len(lines), digest: hex.EncodeToString(h.Sum(nil))}
}

// pending returns the queue's pending set.
func (f *dryRunFixture) pending(t *testing.T) []PendingEntry {
	t.Helper()
	entries, err := f.queue.GetPending(context.Background())
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	return entries
}

// TestDryRunNoAuditEntry proves a simulation of every verdict adds no row
// to the real B-storage audit domain — nor to the policy domain, since a
// simulation must not write a grant or a spent nonce either.
//
// The queue is primed with one real approval first, so the assertion is
// made against a store that is NOT empty: a comparison of two empty
// domains would pass even if the write path were unreachable.
func TestDryRunNoAuditEntry(t *testing.T) {
	ctx := context.Background()
	f := newDryRunFixture(t)
	if err := f.grants.Grant(ctx, Grant{
		Subject: testSubject(), Capability: readCap().Name, ScopeClass: corpus.VisibilityShared,
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := f.queue.Enqueue(ctx, askRequest("prime.txt")); err != nil {
		t.Fatalf("priming the queue: %v", err)
	}

	before := f.snapshot(t)
	if before.rows == 0 {
		t.Fatal("the fixture wrote nothing, so this test could not detect a write")
	}
	for _, in := range []DryRunInput{
		simInput(readCap().Name, L0),
		simInput(approvalCap().Name, L2),
		simInput(destructiveCap().Name, L0),
		simInput(readCap().Name, 0),
	} {
		f.simulate(t, in)
	}
	if after := f.snapshot(t); after != before {
		t.Fatalf("a simulation changed durable state: before %+v, after %+v", before, after)
	}
}

// TestDryRunNoQueueMutation proves the approval queue is untouched: no new
// pending entry, no change to an existing one, and no token minted — even
// for the ask verdict, which is the only verdict whose live path enqueues.
func TestDryRunNoQueueMutation(t *testing.T) {
	ctx := context.Background()
	f := newDryRunFixture(t)
	if _, err := f.queue.Enqueue(ctx, askRequest("prime.txt")); err != nil {
		t.Fatalf("priming the queue: %v", err)
	}
	before := f.pending(t)
	if len(before) != 1 {
		t.Fatalf("the fixture queued %d entries, want 1", len(before))
	}

	res := f.simulate(t, simInput(approvalCap().Name, L2))
	if res.Verdict != VerdictAsk {
		t.Fatalf("report = %+v, want the ask verdict that reaches the queue", res)
	}
	after := f.pending(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a simulation changed the pending set:\nbefore %+v\nafter  %+v", before, after)
	}

	// And the live path, run afterwards on the same action, still enqueues
	// normally: the simulation neither consumed nor poisoned the slot.
	if _, err := f.queue.Enqueue(ctx, askRequest("after.txt")); err != nil {
		t.Fatalf("the live path after a simulation: %v", err)
	}
	if got := len(f.pending(t)); got != 2 {
		t.Fatalf("pending = %d entries after one live enqueue, want 2", got)
	}
}

// TestDryRunDoesNotWidenVisibility proves a simulation discloses nothing
// about a principal that is not the one simulated: a subject with no
// entitlement is refused, is told nothing about the grant somebody else
// holds on the same capability, and cannot use the report to learn that
// such a grant exists.
func TestDryRunDoesNotWidenVisibility(t *testing.T) {
	ctx := context.Background()
	f := newDryRunFixture(t)
	if err := f.grants.Grant(ctx, Grant{
		Subject: testSubject(), Capability: approvalCap().Name, ScopeClass: corpus.VisibilityTeam,
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	stranger := DryRunInput{Request: EvalRequest{
		Subject:    Subject{Kind: SubjectAgent, ID: "lane-z"},
		Capability: approvalCap().Name,
		Level:      L2,
		Action:     "write a.txt",
		Summary:    "write a.txt",
	}}
	res := f.simulate(t, stranger)
	if res.Verdict == VerdictAllow {
		t.Fatalf("an unentitled subject was told the action would be allowed: %+v", res)
	}
	if len(res.ApplicableGrants) != 0 {
		t.Fatalf("the report named %d grants the subject does not hold: %+v",
			len(res.ApplicableGrants), res.ApplicableGrants)
	}
	if res.EffectiveScope != corpus.VisibilityPrivate {
		t.Errorf("EffectiveScope = %q for a subject holding nothing, want private", res.EffectiveScope)
	}
	if res.MatchedRule == LayerStandingGrant.String() {
		t.Error("the report attributed the decision to a grant the subject does not hold")
	}

	// The entitled subject's own report DOES name its grant, so the
	// absence above is scoping and not an unconditional blank.
	own := f.simulate(t, simInput(approvalCap().Name, L2))
	if len(own.ApplicableGrants) != 1 || own.ApplicableGrants[0].Capability != approvalCap().Name {
		t.Fatalf("the holder's own report = %+v, want its one grant", own.ApplicableGrants)
	}
	if !own.ApplicableGrants[0].Matched {
		t.Error("the deciding grant is not marked as the one that matched")
	}
}

// TestDryRunEmptySinkHoldsNothing covers the discarding sink's remaining
// surface. These methods are not on the engine's path; they exist because
// the sink is an ApprovalQueue, and each answers truthfully for a queue
// that filed nothing rather than pretending to be a real queue.
func TestDryRunEmptySinkHoldsNothing(t *testing.T) {
	ctx := context.Background()
	d := &discardEffects{}
	if entries, err := d.GetPending(ctx); err != nil || entries != nil {
		t.Errorf("GetPending = %v, %v; want no entries and no error", entries, err)
	}
	if n, err := d.Expire(ctx); err != nil || n != 0 {
		t.Errorf("Expire = %d, %v; want 0 and no error", n, err)
	}
	if _, err := d.Decide(ctx, nil); err == nil {
		t.Error("Decide on a simulation returned no error")
	}
	if err := d.Cancel(ctx, "req-0001"); err == nil {
		t.Error("Cancel on a simulation returned no error")
	}
	if _, err := d.ConsumeToken(ctx, ConsumeRequest{RequestID: "req-0001"}); err == nil {
		t.Error("a simulation minted a redeemable approval")
	}
	if d.wouldEmitAudit() {
		t.Error("a sink that admitted nothing claims the live path would write")
	}
	// A nil queue is previewed as a refusal rather than by dereferencing.
	var nilQueue *StoreApprovals
	if _, err := nilQueue.previewEnqueue(ctx, askRequest("a.txt")); err == nil {
		t.Error("previewing a nil queue returned no error")
	}
	if nilQueue.wouldRecord() {
		t.Error("a nil queue claims to record")
	}
}
