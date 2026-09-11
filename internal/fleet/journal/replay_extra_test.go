package journal

// Purpose: scanEntityFrom's storage-failure and corrupted-entry branches,
//
//	Replay's own recovery-failure propagation, and a read-only assertion:
//	Replay must never mutate the log it reads (AGENT-BRIEF's "replay is
//	not re-execution" rule).
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

// TestJournalReplay_RecoveryErrorPropagates proves Replay refuses when the
// entity's first-touch recovery scan fails, rather than replaying against
// a log it never actually inspected for a torn tail.
func TestJournalReplay_RecoveryErrorPropagates(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.failGet[entryKey("e", 1)] = errUnavailable("scan disk fault")
	store := New(fs, testkit.NewFrozenClock(testInstant), DefaultNamespace)

	_, err := store.Replay(ctx, "e", Cursor{}, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Replay with a failing recovery scan: err = %v, want KindUnavailable", err)
	}
}

// TestJournalScanEntityFrom_HeadReadError proves scanEntityFrom's own head
// read failing is refused. Recovery for "e" is warmed first on the same
// store instance so its cache absorbs the only other s.store.Get call
// Replay would otherwise make, isolating the failure to scanEntityFrom.
func TestJournalScanEntityFrom_HeadReadError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	store := New(fs, testkit.NewFrozenClock(testInstant), DefaultNamespace)
	if _, err := store.Append(ctx, "e", KindIntent, "op", nil); err != nil {
		t.Fatalf("seeding Append: %v", err)
	}
	fs.failGet[headKey("e")] = errUnavailable("head read disk fault")

	_, err := store.Replay(ctx, "e", Cursor{EntityID: "e", Seq: 0}, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Replay with a failing head read: err = %v, want KindUnavailable", err)
	}
}

// TestJournalScanEntityFrom_EntryReadError proves a non-NotFound failure
// reading one entry in range during Replay's scan is refused, not
// silently skipped as if that sequence were a recovery-truncated gap.
func TestJournalScanEntityFrom_EntryReadError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	store := New(fs, testkit.NewFrozenClock(testInstant), DefaultNamespace)
	for i := 0; i < 3; i++ {
		if _, err := store.Append(ctx, "e", KindIntent, "op", nil); err != nil {
			t.Fatalf("seeding Append %d: %v", i, err)
		}
	}
	fs.failGet[entryKey("e", 2)] = errUnavailable("entry read disk fault")

	_, err := store.Replay(ctx, "e", Cursor{EntityID: "e", Seq: 0}, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Replay with a failing entry read: err = %v, want KindUnavailable", err)
	}
}

// TestJournalScanEntityFrom_DecodeError proves an entry that reads back
// but fails checksum verification is refused (KindIntegrity), using the
// real SQLite driver: the entry is corrupted directly through the
// underlying provider.Store, bypassing Append, exactly as
// TestJournalHeadRecordCorrupted does for the head record. Recovery for
// "e" is warmed by the seeding Append before the corruption is
// introduced, so the torn-tail scan (which runs once per entity and is
// then cached) never itself sees the corrupted bytes — isolating the
// failure to scanEntityFrom's own read.
func TestJournalScanEntityFrom_DecodeError(t *testing.T) {
	ctx := context.Background()
	store, pstore, _ := newTestStore(t)
	if _, err := store.Append(ctx, "e", KindIntent, "op-1", nil); err != nil {
		t.Fatalf("seeding Append: %v", err)
	}
	if err := pstore.Put(ctx, DefaultNamespace, entryKey("e", 1), []byte("not json")); err != nil {
		t.Fatalf("corrupting entry 1: %v", err)
	}

	_, err := store.Replay(ctx, "e", Cursor{EntityID: "e", Seq: 0}, nil)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Replay over a corrupted entry: err = %v, want KindIntegrity", err)
	}
}

// TestJournalReplay_ReadOnly proves Replay never mutates the log it
// reads: the head pointer and entry count are unchanged, and calling
// Replay repeatedly returns byte-identical results. This is the
// AGENT-BRIEF's "replay is not re-execution" contract: a historical read
// must never re-run an escalation or re-send anything, and re-numbering
// or re-writing entries on read would be exactly that kind of side
// effect.
func TestJournalReplay_ReadOnly(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	for i := 0; i < 4; i++ {
		// Distinct operation ids: Replay dedupes by (kind, operation_id),
		// and this test's assertion is about mutation, not dedup, so the
		// fixture must not collide with that unrelated behavior.
		op := fmt.Sprintf("op-%d", i)
		if _, err := store.Append(ctx, "e", KindIntent, op, nil); err != nil {
			t.Fatalf("seeding Append %d: %v", i, err)
		}
	}
	headBefore, err := store.HeadSeq(ctx, "e")
	if err != nil {
		t.Fatalf("HeadSeq before Replay: %v", err)
	}

	first, err := store.Replay(ctx, "e", Cursor{EntityID: "e", Seq: 0}, nil)
	if err != nil {
		t.Fatalf("Replay (first): %v", err)
	}
	second, err := store.Replay(ctx, "e", Cursor{EntityID: "e", Seq: 0}, nil)
	if err != nil {
		t.Fatalf("Replay (second): %v", err)
	}

	headAfter, err := store.HeadSeq(ctx, "e")
	if err != nil {
		t.Fatalf("HeadSeq after Replay: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("Replay changed the head pointer: before=%d after=%d", headBefore, headAfter)
	}
	if len(first) != 4 || len(second) != 4 {
		t.Fatalf("Replay entry counts = %d, %d, want 4 both times", len(first), len(second))
	}
	for i := range first {
		if first[i].Seq != second[i].Seq || first[i].Checksum != second[i].Checksum {
			t.Fatalf("Replay is not repeatable: first[%d]=%+v second[%d]=%+v", i, first[i], i, second[i])
		}
	}
}
