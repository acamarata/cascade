package journal

// Purpose: Replay's cursor-loss fallback, the kinds filter, idempotent-by-
//
//	operation_id deduplication, replay on an empty journal, and
//	checksum verification on a read that goes through the store.
//
// SPORT: internal.fleet.journal.Store/CHANGED (Replay) (tests)
//
//	(P1-E13-W3-S27-T1).

import (
	"context"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestJournalReplayRequiresEntityID(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	_, err := store.Replay(ctx, "", Cursor{}, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Replay with no entity id: err = %v, want KindInvalidInput", err)
	}
}

func TestJournalReplayEmptyJournal(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	entries, err := store.Replay(ctx, "never-touched", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay on an empty journal: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Replay on an empty journal = %+v, want none", entries)
	}
}

func TestJournalCursorLossFallback(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	for i := 0; i < 5; i++ {
		op := fmt.Sprintf("op-%d", i)
		if _, err := store.Append(ctx, "e", KindNodeStream, op, nil); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 3}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// An absent/stale cursor (here: the zero value, naming no entity)
	// must fall back to the persisted checkpoint (seq 3), not to seq 0
	// and not to an error, and must not drop entries 4 and 5.
	entries, err := store.Replay(ctx, "e", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay with a stale cursor: %v", err)
	}
	if len(entries) != 2 || entries[0].Seq != 4 || entries[1].Seq != 5 {
		t.Fatalf("Replay with a stale cursor = %+v, want [seq 4, seq 5]", entries)
	}

	// A cursor naming a different entity is equally stale for this
	// entity and must fall back the same way.
	entries, err = store.Replay(ctx, "e", Cursor{EntityID: "other-entity", Seq: 5}, nil)
	if err != nil {
		t.Fatalf("Replay with a cursor naming another entity: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Replay with a cursor naming another entity = %+v, want 2 entries from the checkpoint fallback", entries)
	}

	// No checkpoint at all falls back to seq 0 (everything).
	entries, err = store.Replay(ctx, "fresh-entity-no-checkpoint", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay with no checkpoint: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Replay with no checkpoint on an untouched entity = %+v, want none", entries)
	}
}

func TestJournalReplayKindsFilter(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	if _, err := store.Append(ctx, "e", KindResumeCursor, "op-1", nil); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if _, err := store.Append(ctx, "e", KindEscalation, "op-2", nil); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if _, err := store.Append(ctx, "e", KindEscalation, "op-3", nil); err != nil {
		t.Fatalf("Append 3: %v", err)
	}
	if _, err := store.Append(ctx, "e", KindNodeStream, "op-4", nil); err != nil {
		t.Fatalf("Append 4: %v", err)
	}

	only, err := store.Replay(ctx, "e", Cursor{}, []Kind{KindEscalation})
	if err != nil {
		t.Fatalf("Replay with kinds filter: %v", err)
	}
	if len(only) != 2 || only[0].Kind != KindEscalation || only[1].Kind != KindEscalation {
		t.Fatalf("Replay([]Kind{KindEscalation}) = %+v, want exactly the two escalation entries", only)
	}

	all, err := store.Replay(ctx, "e", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay with empty kinds (all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("Replay(nil kinds) = %+v, want all 4 entries", all)
	}
}

func TestJournalReplayIdempotentByOperationID(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	if _, err := store.Append(ctx, "e", KindIntent, "op-1", nil); err != nil {
		t.Fatalf("Append intent: %v", err)
	}
	// A caller that could not tell whether its first Append committed
	// retries with the SAME operation_id. Both land in the log (this
	// package does not refuse a repeated operation_id at write time —
	// see full_desc's "at-least-once delivery"), but Replay must hand a
	// downstream applier only one of them.
	if _, err := store.Append(ctx, "e", KindIntent, "op-1", nil); err != nil {
		t.Fatalf("Append retried intent: %v", err)
	}
	if _, err := store.Append(ctx, "e", KindAck, "op-2", nil); err != nil {
		t.Fatalf("Append distinct op: %v", err)
	}

	entries, err := store.Replay(ctx, "e", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Replay = %+v, want exactly 2 entries (op-1 once, op-2 once)", entries)
	}
	if entries[0].OperationID != "op-1" || entries[0].Seq != 1 {
		t.Fatalf("Replay[0] = %+v, want the FIRST op-1 entry (seq 1)", entries[0])
	}
	if entries[1].OperationID != "op-2" {
		t.Fatalf("Replay[1] = %+v, want op-2", entries[1])
	}
}

func TestJournalChecksumVerifiedOnRead(t *testing.T) {
	ctx := context.Background()
	store, pstore, _ := newTestStore(t)

	if _, err := store.Append(ctx, "e", KindNodeStream, "op-1", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Corrupt the committed entry directly, bypassing this package's API
	// entirely, then force a fresh recovery pass by using a new SQLiteStore
	// instance (recovery runs once per entity per instance).
	if err := pstore.Put(ctx, DefaultNamespace, entryKey("e", 1), []byte(`{"entity_id":"e","seq":1,"kind":8,"operation_id":"op-1","checksum":"deadbeef"}`)); err != nil {
		t.Fatalf("corrupting entry 1: %v", err)
	}

	fresh := New(pstore, testkit.NewFrozenClock(testInstant), DefaultNamespace)
	report, err := fresh.Recover(ctx, "e")
	if err != nil {
		t.Fatalf("Recover over a corrupted entry: %v", err)
	}
	if report.Truncated != 1 || report.FirstBadSeq != 1 {
		t.Fatalf("Recover report = %+v, want {Truncated:1 FirstBadSeq:1}", report)
	}
	entries, err := fresh.Replay(ctx, "e", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay after recovery: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Replay after recovery = %+v, want none (the only entry was corrupted)", entries)
	}
}
