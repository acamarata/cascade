package journal

// Purpose: scanAndTruncate's and repairHead's storage-failure branches
//
//	(entry.go), none of which the real SQLite driver can deterministically
//	produce (a Get or Delete failing at one specific key while every other
//	key still reads and writes fine). Uses fakeStore (fakestore_test.go).
//
// SPORT: internal.fleet.journal.Store/ADDED (tests) (P1-E13-W3-S27-T1).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestJournalScanAndTruncate_GetError proves a non-NotFound failure
// reading an entry during the torn-tail scan is refused (wrapStore),
// never silently treated as "log ends here".
func TestJournalScanAndTruncate_GetError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.failGet[entryKey("e", 1)] = errUnavailable("scan disk fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	_, err := store.Recover(ctx, "e")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Recover with a failing scan Get: err = %v, want KindUnavailable", err)
	}
}

// TestJournalScanAndTruncate_DeleteError proves a failure deleting a
// corrupted trailing entry is refused rather than leaving the corrupted
// entry in place while the caller believes recovery succeeded.
func TestJournalScanAndTruncate_DeleteError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	if err := fs.Put(ctx, DefaultNamespace, entryKey("e", 1), []byte("not json")); err != nil {
		t.Fatalf("seeding corrupted entry: %v", err)
	}
	fs.failDelete[entryKey("e", 1)] = errUnavailable("delete disk fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	_, err := store.Recover(ctx, "e")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Recover with a failing scan Delete: err = %v, want KindUnavailable", err)
	}
}

// TestJournalRepairHead_PutError proves a failure writing the repaired
// (lowered) head pointer after a torn-tail truncation is refused, rather
// than leaving the head pointer claiming coverage the truncated log no
// longer has.
func TestJournalRepairHead_PutError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	// Seed a head pointer claiming seq 1, but no entry 1 on disk: the
	// scan finds lastGood 0, disagreeing with the seeded head of 1, so
	// repairHead must write a lowered head record.
	seedHead(ctx, t, fs, "e", 1)
	fs.failPut[headKey("e")] = errUnavailable("head repair disk fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	_, err := store.Recover(ctx, "e")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Recover with a failing head repair: err = %v, want KindUnavailable", err)
	}
}

// TestJournalCheckpoint_RecoveryErrorPropagates proves Checkpoint refuses
// when the entity's first-touch recovery scan fails, rather than
// publishing a cursor against a log it never actually inspected.
func TestJournalCheckpoint_RecoveryErrorPropagates(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.failGet[entryKey("e", 1)] = errUnavailable("scan disk fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 0})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Checkpoint with a failing recovery scan: err = %v, want KindUnavailable", err)
	}
}

// TestJournalCheckpoint_HeadReadErrorInTx proves checkpointTx refuses when
// it cannot read the entity's head inside its own transaction, rather
// than defaulting to head 0 and accepting a checkpoint the log cannot
// support. Recovery for "e" is deliberately warmed (and cached) by a
// prior Append on the SAME store instance first, so this Checkpoint
// call's recoverEntityLocked returns from cache and never touches
// s.store.Get itself — isolating the failure to checkpointTx's own
// in-transaction head read.
func TestJournalCheckpoint_HeadReadErrorInTx(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	store := New(fs, testkit.NewFrozenClock(testInstant), DefaultNamespace)
	if _, err := store.Append(ctx, "e", KindIntent, "op", nil); err != nil {
		t.Fatalf("seeding Append: %v", err)
	}
	fs.failGet[headKey("e")] = errUnavailable("head read disk fault")

	err := store.Checkpoint(ctx, Cursor{EntityID: "e", Seq: 0})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Checkpoint with a failing in-tx head read: err = %v, want KindUnavailable", err)
	}
}

// seedHead writes entityID's head pointer directly, bypassing Append, so
// a test can construct a head/log disagreement recovery must repair.
func seedHead(ctx context.Context, t *testing.T, fs *fakeStore, entityID string, seq uint64) {
	t.Helper()
	data, err := json.Marshal(headRecord{Seq: seq})
	if err != nil {
		t.Fatalf("seedHead: %v", err)
	}
	if err := fs.Put(ctx, DefaultNamespace, headKey(entityID), data); err != nil {
		t.Fatalf("seedHead: Put: %v", err)
	}
}
