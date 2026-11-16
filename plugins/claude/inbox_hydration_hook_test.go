package claude

// Purpose: proves AppendNextTurnWith's beforeCommit hook: it sees the records
// the drain will return while they are still queued, its writes through
// DrainTx commit with the drain, and its error aborts the drain with the
// queue unchanged and nothing returned.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestInboxDrainMarksTaintBeforeCommit: beforeCommit sees the untrusted records
// while still queued; its write through the drain transaction commits with it.
func TestInboxDrainMarksTaintBeforeCommit(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()
	h, _ := NewInboxHydration(mem, func() time.Time { return fixedNow })
	mustEnqueue(t, h, "s1", InboxEvent{ID: "u1", Class: "addressed", TargetSession: "s1", DataClass: provider.SensitivityInternal})
	mustEnqueue(t, h, "s1", InboxEvent{ID: "lo", Class: "addressed", TargetSession: "s1", DataClass: provider.SensitivityLocalOnly})
	recs, res, err := h.AppendNextTurnWith(ctx, "s1", func(ctx context.Context, recs []InboxRecord) error {
		tx := DrainTx(ctx)
		if queued, err := tx.Get(ctx, HydrationNamespace, "s1"); err != nil || len(queued) == 0 {
			t.Fatalf("the queue was gone before beforeCommit ran: (%q, %v)", queued, err)
		}
		if len(recs) != 1 || !recs[0].Untrusted() || !(InboxRecord{}).Untrusted() || (InboxRecord{Envelope: Envelope{Trust: "trusted"}}).Untrusted() {
			t.Fatalf("beforeCommit got %+v, want only the one returned (untrusted) record", recs)
		}
		return tx.Put(ctx, "test.taint", "s1", []byte(recs[0].ID))
	})
	if err != nil || !res.Appended || res.Withheld != 1 || len(recs) != 1 || recs[0].ID != "u1" {
		t.Fatalf("drain = (%+v, %+v, %v)", recs, res, err)
	}
	if mark, err := mem.Get(ctx, "test.taint", "s1"); err != nil || string(mark) != "u1" {
		t.Fatalf("the mark did not commit with the drain: (%q, %v)", mark, err)
	}
}

// TestInboxDrainAbortsOnTaintError: a beforeCommit error aborts the drain: the
// queue stays byte-identical, the hook's write rolls back, nothing returns.
func TestInboxDrainAbortsOnTaintError(t *testing.T) {
	ctx := context.Background()
	mem := storetest.NewMemStore()
	h, _ := NewInboxHydration(mem, func() time.Time { return fixedNow })
	mustEnqueue(t, h, "s1", InboxEvent{ID: "u1", Class: "addressed", TargetSession: "s1", DataClass: provider.SensitivityInternal})
	before, _ := mem.Get(ctx, HydrationNamespace, "s1")
	markErr := cascade.New(cascade.KindConflict, "test: mark refused")
	recs, res, err := h.AppendNextTurnWith(ctx, "s1", func(ctx context.Context, _ []InboxRecord) error {
		_ = DrainTx(ctx).Put(ctx, "test.taint", "s1", []byte("half"))
		return markErr
	})
	if err != markErr || err.Error() != "conflict: test: mark refused" || recs != nil || res.Appended || DrainTx(ctx) != nil {
		t.Fatalf("aborted drain = (%+v, %+v, %v), want nothing and the hook's error", recs, res, err)
	}
	if after, _ := mem.Get(ctx, HydrationNamespace, "s1"); len(before) == 0 || string(after) != string(before) {
		t.Fatalf("queue changed: %q -> %q", before, after)
	}
	if _, err := mem.Get(ctx, "test.taint", "s1"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("the hook's write survived the abort: %v", err)
	}
}
