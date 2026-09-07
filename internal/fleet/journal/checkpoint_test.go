package journal

// Purpose: Checkpoint's idempotent re-checkpoint case, the atomic
//
//	cursor-plus-covered-sequence write, and the beyond-log refusal.
//
// SPORT: internal.fleet.journal.Store/CHANGED (Checkpoint) (tests)
//
//	(P1-E13-W3-S27-T1).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestJournalIdempotentCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, pstore, _ := newTestStore(t)

	if _, err := store.Append(ctx, "e", KindNodeStream, "op-1", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 1}); err != nil {
		t.Fatalf("first Checkpoint: %v", err)
	}
	before, ok, err := store.loadCheckpointFrom(ctx, pstore, "e")
	if err != nil || !ok {
		t.Fatalf("loadCheckpointFrom after first Checkpoint: ok=%v err=%v", ok, err)
	}

	if err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 1}); err != nil {
		t.Fatalf("second (idempotent) Checkpoint: %v", err)
	}
	after, ok, err := store.loadCheckpointFrom(ctx, pstore, "e")
	if err != nil || !ok {
		t.Fatalf("loadCheckpointFrom after second Checkpoint: ok=%v err=%v", ok, err)
	}
	if before != after {
		t.Fatalf("idempotent re-checkpoint changed the record: before=%+v after=%+v", before, after)
	}
}

func TestJournalCheckpointAtomicWithCoveredSequence(t *testing.T) {
	ctx := context.Background()
	store, pstore, _ := newTestStore(t)

	for i := 0; i < 3; i++ {
		if _, err := store.Append(ctx, "e", KindNodeStream, "op", nil); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 2}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	rec, ok, err := store.loadCheckpointFrom(ctx, pstore, "e")
	if err != nil || !ok {
		t.Fatalf("loadCheckpointFrom: ok=%v err=%v", ok, err)
	}
	if rec.Seq != 2 || rec.CoveredSeq != 3 {
		t.Fatalf("checkpoint record = %+v, want {Seq:2 CoveredSeq:3}", rec)
	}

	err = store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 99})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Checkpoint beyond log head: err = %v, want KindConflict (ErrCheckpointBeyondLog)", err)
	}
	// The refused checkpoint must not have overwritten the prior valid
	// record: a reader must never observe a cursor claiming coverage the
	// log does not have.
	after, ok, err := store.loadCheckpointFrom(ctx, pstore, "e")
	if err != nil || !ok || after != rec {
		t.Fatalf("checkpoint record after a refused Checkpoint = %+v ok=%v, want unchanged %+v", after, ok, rec)
	}
}

func TestJournalCheckpointRequiresEntityID(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	err := store.Checkpoint(ctx, Cursor{})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Checkpoint with no entity id: err = %v, want KindInvalidInput", err)
	}
}

func TestJournalCheckpointRecordCorrupted(t *testing.T) {
	ctx := context.Background()
	store, pstore, _ := newTestStore(t)

	if _, err := store.Append(ctx, "e", KindNodeStream, "op-1", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := pstore.Put(ctx, DefaultNamespace, checkpointKey("e"), []byte("not json")); err != nil {
		t.Fatalf("seeding corrupted checkpoint: %v", err)
	}
	if _, err := store.Replay(ctx, "e", Cursor{}, nil); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Replay with a corrupted checkpoint fallback: err = %v, want KindIntegrity", err)
	}
	if err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 1}); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Checkpoint over a corrupted existing checkpoint: err = %v, want KindIntegrity", err)
	}
}
