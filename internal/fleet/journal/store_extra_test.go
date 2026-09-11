package journal

// Purpose: HeadSeq and ListEntities (both CHANGE, P1-E13-W3-S27-T2) plus
//
//	commitEntryTx's head-write failure branch — none of these had any
//	direct test coverage. Uses fakeStore (fakestore_test.go) wherever the
//	behavior under test is a Put/Scan/Get failure the real SQLite driver
//	cannot deterministically produce; the real driver otherwise (Art.2
//	real counterpart).
//
// SPORT: internal.fleet.journal.Store/ADDED (tests) (P1-E13-W3-S27-T2).

import (
	"context"
	"sort"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestJournalHeadSeq_NeverAppended(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	seq, err := store.HeadSeq(ctx, "ghost")
	if err != nil {
		t.Fatalf("HeadSeq on a never-appended entity: %v", err)
	}
	if seq != 0 {
		t.Fatalf("HeadSeq on a never-appended entity = %d, want 0", seq)
	}
}

func TestJournalHeadSeq_AfterAppend(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	for i := 0; i < 3; i++ {
		if _, err := store.Append(ctx, "e", KindIntent, "op", nil); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	seq, err := store.HeadSeq(ctx, "e")
	if err != nil {
		t.Fatalf("HeadSeq: %v", err)
	}
	if seq != 3 {
		t.Fatalf("HeadSeq = %d, want 3", seq)
	}
}

// TestJournalHeadSeq_RecoveryError proves HeadSeq propagates a failure
// from its own first-touch recovery scan rather than reporting a false
// head of 0 (which would read as "unknown entity" to callers like
// resolveEntries).
func TestJournalHeadSeq_RecoveryError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.failGet[entryKey("e", 1)] = errUnavailable("scan disk fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	if _, err := store.HeadSeq(ctx, "e"); err == nil {
		t.Fatal("HeadSeq: want an error when the recovery scan cannot read the log, got nil")
	} else if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("HeadSeq recovery error = %v, want KindUnavailable", err)
	}
}

func TestJournalListEntities_Empty(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	got, err := store.ListEntities(ctx)
	if err != nil {
		t.Fatalf("ListEntities on an empty store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListEntities on an empty store = %v, want none", got)
	}
}

func TestJournalListEntities_HappyPath(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	for _, id := range []string{"bravo", "alpha", "charlie"} {
		if _, err := store.Append(ctx, id, KindIntent, "op", nil); err != nil {
			t.Fatalf("Append(%s): %v", id, err)
		}
	}
	got, err := store.ListEntities(ctx)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	sort.Strings(got)
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("ListEntities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListEntities = %v, want %v", got, want)
		}
	}
}

func TestJournalListEntities_ScanError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.failScan = errUnavailable("scan disk fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	if _, err := store.ListEntities(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ListEntities with a failing Scan: err = %v, want KindUnavailable", err)
	}
}

func TestJournalListEntities_IteratorError(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)
	if _, err := store.Append(ctx, "e", KindIntent, "op", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	fs.iterErr = errUnavailable("mid-scan disk fault")

	if _, err := store.ListEntities(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ListEntities with a failing iterator: err = %v, want KindUnavailable", err)
	}
}

// TestJournalAppend_HeadWriteFailure proves commitEntryTx's second write
// (the advanced head pointer) failing surfaces as an error and is not
// silently swallowed after the entry itself already landed.
func TestJournalAppend_HeadWriteFailure(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.failPut[headKey("e")] = errUnavailable("head write fault")
	clock := testkit.NewFrozenClock(testInstant)
	store := New(fs, clock, DefaultNamespace)

	_, err := store.Append(ctx, "e", KindIntent, "op-1", nil)
	if err == nil {
		t.Fatal("Append: want an error when the head write fails, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Append head-write-failure error = %v, want KindUnavailable", err)
	}
}

// errUnavailable is a plain error (not a *cascade.Error), matching what a
// real storage driver failure looks like before this package's own
// wrapStore classifies it.
type errUnavailable string

func (e errUnavailable) Error() string { return string(e) }
