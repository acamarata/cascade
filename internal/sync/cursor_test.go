package sync

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newTestCursorStore() *CursorStore {
	return NewCursorStore(newFakeStore(), fakeClock{t: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)})
}

func TestCursorGetDefaultsZero(t *testing.T) {
	cs := newTestCursorStore()
	cur, err := cs.Get(context.Background(), storage.DomainMemory, "memory")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cur.Position != 0 {
		t.Fatalf("fresh cursor position = %d, want 0", cur.Position)
	}
}

func TestCursorAdvanceResumableAcrossRuns(t *testing.T) {
	store := newFakeStore()
	cs1 := NewCursorStore(store, fakeClock{t: time.Now()})
	ctx := context.Background()
	if err := cs1.Advance(ctx, storage.DomainMemory, "memory", 5); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	// A fresh CursorStore instance over the SAME store reads back the
	// persisted position — resumable across process runs.
	cs2 := NewCursorStore(store, fakeClock{t: time.Now()})
	cur, err := cs2.Get(ctx, storage.DomainMemory, "memory")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cur.Position != 5 {
		t.Fatalf("resumed position = %d, want 5", cur.Position)
	}
}

func TestCursorRegressRefusedAsTypedConflict(t *testing.T) {
	cs := newTestCursorStore()
	ctx := context.Background()
	if err := cs.Advance(ctx, storage.DomainMemory, "memory", 10); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	err := cs.Advance(ctx, storage.DomainMemory, "memory", 3)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("regress: want KindConflict, got %v", err)
	}
	cur, _ := cs.Get(ctx, storage.DomainMemory, "memory")
	if cur.Position != 10 {
		t.Fatalf("a refused regress must not move the cursor: got %d, want 10", cur.Position)
	}
}

func TestSyncCursorAdvancesOverExclusions(t *testing.T) {
	cs := newTestCursorStore()
	ctx := context.Background()
	excl := Exclusion{RecordID: "rec-1", Reason: "sensitivity-restricted", PolicyVersion: 1}
	if err := cs.AdvanceOverExclusion(ctx, storage.DomainConfig, "accounts", excl, 1); err != nil {
		t.Fatalf("AdvanceOverExclusion: %v", err)
	}
	cur, err := cs.Get(ctx, storage.DomainConfig, "accounts")
	if err != nil || cur.Position != 1 {
		t.Fatalf("cursor did not advance past the excluded record: %+v, err=%v", cur, err)
	}
	got, err := cs.Exclusion(ctx, storage.DomainConfig, "accounts", "rec-1")
	if err != nil {
		t.Fatalf("Exclusion: %v", err)
	}
	if got.Reason != "sensitivity-restricted" || got.PolicyVersion != 1 {
		t.Fatalf("exclusion not journaled faithfully: %+v", got)
	}
}

func TestSyncExclusionRescanOnPolicyChange(t *testing.T) {
	cs := newTestCursorStore()
	ctx := context.Background()
	excl := Exclusion{RecordID: "rec-2", Reason: "sensitivity-local-only", PolicyVersion: 1}
	if err := cs.AdvanceOverExclusion(ctx, storage.DomainConfig, "accounts", excl, 1); err != nil {
		t.Fatalf("AdvanceOverExclusion: %v", err)
	}
	stored, err := cs.Exclusion(ctx, storage.DomainConfig, "accounts", "rec-2")
	if err != nil {
		t.Fatalf("Exclusion: %v", err)
	}
	if cs.NeedsRescan(stored, 1) {
		t.Fatal("same policy version must not need a rescan")
	}
	if !cs.NeedsRescan(stored, 2) {
		t.Fatal("a later policy version must trigger a rescan of the excluded record")
	}
}

func TestCursorGetCorruptPersistedStateFailsClosed(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	_ = store.Put(ctx, cursorNamespace, cursorKey(storage.DomainMemory, "memory"), []byte("not json"))
	cs := NewCursorStore(store, fakeClock{t: time.Now()})
	if _, err := cs.Get(ctx, storage.DomainMemory, "memory"); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("corrupt cursor: want KindIntegrity, got %v", err)
	}
}

func TestCursorAdvancePropagatesStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.failPut = true
	cs := NewCursorStore(store, fakeClock{t: time.Now()})
	if err := cs.Advance(context.Background(), storage.DomainMemory, "memory", 1); err == nil {
		t.Fatal("Advance must propagate a store Put failure")
	}
}

func TestCursorExclusionCorruptPersistedStateFailsClosed(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	_ = store.Put(ctx, cursorNamespace, exclusionKey(storage.DomainConfig, "accounts", "rec-1"), []byte("not json"))
	cs := NewCursorStore(store, fakeClock{t: time.Now()})
	if _, err := cs.Exclusion(ctx, storage.DomainConfig, "accounts", "rec-1"); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("corrupt exclusion: want KindIntegrity, got %v", err)
	}
}

func TestListExclusionsPropagatesScanFailure(t *testing.T) {
	store := newFakeStore()
	store.failScan = true
	cs := NewCursorStore(store, fakeClock{t: time.Now()})
	if _, err := cs.ListExclusions(context.Background(), storage.DomainConfig, "accounts"); err == nil {
		t.Fatal("ListExclusions must propagate a store Scan failure")
	}
}

func TestListExclusionsCorruptEntryFailsClosed(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	_ = store.Put(ctx, cursorNamespace, "sync/exclusion/config/accounts/rec-x", []byte("not json"))
	cs := NewCursorStore(store, fakeClock{t: time.Now()})
	if _, err := cs.ListExclusions(ctx, storage.DomainConfig, "accounts"); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("corrupt scanned exclusion: want KindIntegrity, got %v", err)
	}
}

func TestListExclusionsScansAll(t *testing.T) {
	cs := newTestCursorStore()
	ctx := context.Background()
	_ = cs.AdvanceOverExclusion(ctx, storage.DomainConfig, "accounts", Exclusion{RecordID: "rec-1", PolicyVersion: 1}, 1)
	_ = cs.AdvanceOverExclusion(ctx, storage.DomainConfig, "accounts", Exclusion{RecordID: "rec-2", PolicyVersion: 1}, 2)
	got, err := cs.ListExclusions(ctx, storage.DomainConfig, "accounts")
	if err != nil {
		t.Fatalf("ListExclusions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
}
