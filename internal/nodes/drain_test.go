package nodes

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRecordStoreDrain_MarksDrainedPreservesRest(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "d")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	rec, err := store.Drain(id.NodeID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rec.Drained {
		t.Fatal("expected Drained=true")
	}
	if rec.Tier != TierWorkerTrusted {
		t.Fatalf("tier changed by drain: got %q", rec.Tier)
	}

	got, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatalf("get after drain: %v", err)
	}
	if !got.Drained {
		t.Fatal("drain was not persisted")
	}
}

func TestRecordStoreDrain_IdempotentNoError(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "e")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if _, err := store.Drain(id.NodeID); err != nil {
		t.Fatalf("first drain: %v", err)
	}
	rec, err := store.Drain(id.NodeID)
	if err != nil {
		t.Fatalf("second drain (idempotent) unexpectedly errored: %v", err)
	}
	if !rec.Drained {
		t.Fatal("expected Drained=true on idempotent re-drain")
	}
}

func TestRecordStoreDrain_UnknownNodeIsNotFound(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)

	_, err := store.Drain("does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown node id")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindNotFound {
		t.Fatalf("expected KindNotFound, got %v (ok=%v)", kind, ok)
	}
}

func TestRecordStoreDrain_BackendLoadErrorPropagates(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, clock)
	id := testIdentity(t, "f")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	backend.loadErr = cascade.New(cascade.KindUnavailable, "boom")

	_, err := store.Drain(id.NodeID)
	if err == nil {
		t.Fatal("expected the backend load error to propagate")
	}
}

// TestRecordStoreDrain_BackendSaveErrorPropagates isolates Drain's OWN
// s.put error branch: Get succeeds (the record is not yet drained), but
// the write that would persist Drained=true fails.
func TestRecordStoreDrain_BackendSaveErrorPropagates(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, clock)
	id := testIdentity(t, "g")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	backend.saveErr = cascade.New(cascade.KindUnavailable, "simulated write failure")

	if _, err := store.Drain(id.NodeID); err == nil {
		t.Fatal("expected the backend save error to propagate")
	}
}
