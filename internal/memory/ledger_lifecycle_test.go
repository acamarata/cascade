package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromoteWritesTheDurableRecordAndEmits(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	observeAll(t, f.ledger, "s-1", "s-2", "s-3")

	got, err := f.ledger.Promote(ctx, KindProject, "a-record")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got.Status != CandidatePromoted || got.PromotedAt == nil {
		t.Errorf("after Promote status=%q PromotedAt=%v", got.Status, got.PromotedAt)
	}
	stored, err := f.store.Read(ctx, KindProject, "a-record")
	if err != nil {
		t.Fatalf("durable record not readable after promotion: %v", err)
	}
	if stored.Body != validEntry().Body || stored.ScopeRef != validEntry().ScopeRef {
		t.Errorf("durable record = %+v, want the observed draft", stored)
	}
	if len(f.sink.promotions) != 1 {
		t.Fatalf("promotion events = %d, want 1", len(f.sink.promotions))
	}
	ev := f.sink.promotions[0]
	if ev.RefCount != 3 || len(ev.SessionIDs) != 3 || ev.PromotedAt != fixedNow {
		t.Errorf("promotion event = %+v, want the evidence at %s", ev, fixedNow)
	}
	if ev.EventName() != "memory.candidate.promoted" {
		t.Errorf("event name = %q", ev.EventName())
	}
}

// TestObserveOnPromotedIsANoOp is R-14.22's first half: a promoted
// candidate does not move, does not emit, and does not write.
func TestObserveOnPromotedIsANoOp(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	observeAll(t, f.ledger, "s-1", "s-2", "s-3")
	before, err := f.ledger.Promote(ctx, KindProject, "a-record")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	path := filepath.Join(f.base, "candidates", "project", "a-record.candidate.json")
	bytesBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading candidate file: %v", err)
	}

	after, err := f.ledger.Observe(ctx, observation("s-4"))
	if err != nil {
		t.Fatalf("Observe on promoted candidate: %v", err)
	}
	if after.RefCount != before.RefCount || len(after.SessionIDs) != len(before.SessionIDs) {
		t.Errorf("promoted candidate moved: %+v, was %+v", after, before)
	}
	if after.Status != CandidatePromoted {
		t.Errorf("status = %q, want %q", after.Status, CandidatePromoted)
	}
	bytesAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading candidate file: %v", err)
	}
	if string(bytesBefore) != string(bytesAfter) {
		t.Error("observing a promoted candidate rewrote its file")
	}
	if len(f.sink.promotions) != 1 || len(f.sink.reverts) != 0 {
		t.Errorf("events after no-op observe: %d promotions, %d reverts",
			len(f.sink.promotions), len(f.sink.reverts))
	}
}

// TestRevertThenObserveRestartsTheCount is R-14.22's second half.
func TestRevertThenObserveRestartsTheCount(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	observeAll(t, f.ledger, "s-1", "s-2", "s-3")
	if _, err := f.ledger.Promote(ctx, KindProject, "a-record"); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	reverted, err := f.ledger.Revert(ctx, KindProject, "a-record", "user said it was wrong")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if reverted.Status != CandidateReverted || reverted.PromotedAt != nil {
		t.Errorf("after Revert status=%q PromotedAt=%v", reverted.Status, reverted.PromotedAt)
	}
	if len(f.sink.reverts) != 1 || f.sink.reverts[0].Reason != "user said it was wrong" {
		t.Fatalf("revert events = %+v", f.sink.reverts)
	}
	if f.sink.reverts[0].EventName() != "memory.candidate.reverted" {
		t.Errorf("event name = %q", f.sink.reverts[0].EventName())
	}

	restarted, err := f.ledger.Observe(ctx, observation("s-9"))
	if err != nil {
		t.Fatalf("Observe after revert: %v", err)
	}
	if restarted.Status != CandidatePending || restarted.RefCount != 1 {
		t.Errorf("after revert+observe status=%q RefCount=%d, want pending and 1",
			restarted.Status, restarted.RefCount)
	}
	if len(restarted.SessionIDs) != 1 || restarted.SessionIDs[0] != "s-9" {
		t.Errorf("SessionIDs = %v, want only the observing session", restarted.SessionIDs)
	}
}

// TestRevertKeepsTheDurableRecordAndTheHistory pins the traceability rule:
// a reverted promotion is accountable afterwards, not merely gone.
func TestRevertKeepsTheDurableRecordAndTheHistory(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	observeAll(t, f.ledger, "s-1", "s-2", "s-3")
	if _, err := f.ledger.Promote(ctx, KindProject, "a-record"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if _, err := f.ledger.Revert(ctx, KindProject, "a-record", "wrong"); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	if _, err := f.store.Read(ctx, KindProject, "a-record"); err != nil {
		t.Errorf("revert removed the durable record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(f.base, "candidates", "project", "a-record.candidate.json"))
	if err != nil {
		t.Fatalf("reading candidate file: %v", err)
	}
	for _, want := range []string{`"revert_reason": "wrong"`, `"reverted_at"`, `"ref_count": 3`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("candidate file lost %s:\n%s", want, raw)
		}
	}
}

func TestPromoteAndRevertConflicts(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	observeAll(t, f.ledger, "s-1", "s-2", "s-3")

	if _, err := f.ledger.Revert(ctx, KindProject, "a-record", ""); !errors.Is(err, ErrNotPromoted) {
		t.Errorf("Revert on a pending candidate: %v, want ErrNotPromoted", err)
	}
	if _, err := f.ledger.Promote(ctx, KindProject, "a-record"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	_, err := f.ledger.Promote(ctx, KindProject, "a-record")
	if !errors.Is(err, ErrAlreadyPromoted) {
		t.Errorf("second Promote: %v, want ErrAlreadyPromoted", err)
	}
	if len(f.sink.promotions) != 1 {
		t.Errorf("a refused second promotion emitted an event: %+v", f.sink.promotions)
	}
}

func TestMissingCandidateIsTyped(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)

	if _, err := f.ledger.Get(ctx, KindProject, "absent"); !errors.Is(err, ErrNoSuchCandidate) {
		t.Errorf("Get: %v, want ErrNoSuchCandidate", err)
	}
	if _, err := f.ledger.Promote(ctx, KindProject, "absent"); !errors.Is(err, ErrNoSuchCandidate) {
		t.Errorf("Promote: %v, want ErrNoSuchCandidate", err)
	}
	if _, err := f.ledger.Revert(ctx, KindProject, "absent", ""); !errors.Is(err, ErrNoSuchCandidate) {
		t.Errorf("Revert: %v, want ErrNoSuchCandidate", err)
	}
}
