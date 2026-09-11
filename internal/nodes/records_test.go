package nodes

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memRecordBackend is an in-memory RecordBackend fake for tests. Also used
// to simulate a corrupt/unavailable store.
type memRecordBackend struct {
	records map[string]DeviceRecord
	loadErr error
	saveErr error
}

func newMemRecordBackend() *memRecordBackend {
	return &memRecordBackend{records: map[string]DeviceRecord{}}
}

func (m *memRecordBackend) Load() (map[string]DeviceRecord, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	out := map[string]DeviceRecord{}
	for k, v := range m.records {
		out[k] = v
	}
	return out, nil
}

func (m *memRecordBackend) Save(records map[string]DeviceRecord) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.records = records
	return nil
}

func testIdentity(t *testing.T, seed string) Identity {
	t.Helper()
	id, _, err := GenerateIdentity(strings.NewReader(strings.Repeat(seed, 64)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecordStoreEnrollAndGet(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "a")

	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Tier != TierWorkerTrusted {
		t.Fatalf("tier = %q, want worker-trusted", rec.Tier)
	}
	if rec.EnrolledAt.IsZero() {
		t.Fatal("EnrolledAt not set from injected clock")
	}

	got, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatalf("unexpected error on Get: %v", err)
	}
	if got.NodeID != id.NodeID {
		t.Fatal("round-trip node id mismatch")
	}
}

// TestRecordStoreReEnrollConflict proves re-enroll of a live identity is a
// typed conflict error, never a silent overwrite (acceptance criterion).
func TestRecordStoreReEnrollConflict(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "b")

	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	_, err := store.Enroll(id, TierController)
	if err == nil {
		t.Fatal("expected conflict on re-enroll, got nil")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindConflict {
		t.Fatalf("expected KindConflict, got %v (ok=%v)", k, ok)
	}
	// The original record must be untouched (never silently overwritten).
	got, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierWorkerTrusted {
		t.Fatalf("record was overwritten: tier = %q, want worker-trusted (unchanged)", got.Tier)
	}
}

func TestRecordStoreGetNotFound(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now()))
	_, err := store.Get("no-such-node")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindNotFound {
		t.Fatalf("expected KindNotFound, got %v (ok=%v)", k, ok)
	}
}

func TestRecordStoreInvalidTierRefused(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "c")
	_, err := store.Enroll(id, Tier("bogus"))
	if err == nil {
		t.Fatal("expected refusal for invalid tier")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
	}
}

func TestRecordStoreBackendUnavailable(t *testing.T) {
	backend := newMemRecordBackend()
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated storage outage")
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))

	id := testIdentity(t, "d")
	_, err := store.Enroll(id, TierWorkerTrusted)
	if err == nil {
		t.Fatal("expected error when backend is unavailable")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

func TestRecordStoreListSorted(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now()))
	idA := testIdentity(t, "e")
	idB := testIdentity(t, "f")
	if _, err := store.Enroll(idA, TierController); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enroll(idB, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 records, got %d", len(list))
	}
	if list[0].NodeID > list[1].NodeID {
		t.Fatal("List did not return sorted order")
	}
}

func TestFileRecordBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileRecordBackend(dir)
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "g")

	if _, err := store.Enroll(id, TierController); err != nil {
		t.Fatal(err)
	}
	// Fresh backend instance over the same dir must read back the record.
	backend2 := NewFileRecordBackend(dir)
	store2 := NewRecordStore(backend2, testkit.NewFrozenClock(time.Now()))
	got, err := store2.Get(id.NodeID)
	if err != nil {
		t.Fatalf("unexpected error reading persisted record: %v", err)
	}
	if got.NodeID != id.NodeID || got.Tier != TierController {
		t.Fatalf("persisted record mismatch: %+v", got)
	}
}

func TestFileRecordBackendCorruptRefused(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileRecordBackend(dir)
	// Force-load with no file yet: must be empty map, not an error.
	records, err := backend.Load()
	if err != nil {
		t.Fatalf("unexpected error on missing file: %v", err)
	}
	if len(records) != 0 {
		t.Fatal("expected empty map for missing file")
	}
}

func TestRecordStoreSaveErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	backend.saveErr = cascade.New(cascade.KindUnavailable, "simulated write failure")
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "h")

	_, err := store.Enroll(id, TierWorkerTrusted)
	if err == nil {
		t.Fatal("expected error when backend save fails")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

func TestFileRecordBackendCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileRecordBackend(dir)
	// Force creation of the file with garbage content by writing directly
	// via the same path a real deployment would use.
	subdir := dir + "/nodes"
	if err := writeGarbage(subdir, "devices.json"); err != nil {
		t.Fatal(err)
	}
	_, err := backend.Load()
	if err == nil {
		t.Fatal("expected error loading corrupt JSON")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

func TestRecordStorePutErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "i")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	backend.saveErr = cascade.New(cascade.KindUnavailable, "simulated write failure")
	if err := store.put(rec); err == nil {
		t.Fatal("expected error from put when backend save fails")
	}
}

func TestRecordStoreRemove_DeletesRecord(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "j")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(id.NodeID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.Get(id.NodeID); err == nil {
		t.Fatal("expected the record to be gone after Remove")
	}
}

func TestRecordStoreRemove_UnknownNodeIsNotFound(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now()))
	err := store.Remove("does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown node id")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindNotFound {
		t.Fatalf("expected KindNotFound, got %v (ok=%v)", k, ok)
	}
}

func TestRecordStoreRemove_BackendSaveErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "k")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	backend.saveErr = cascade.New(cascade.KindUnavailable, "simulated write failure")
	if err := store.Remove(id.NodeID); err == nil {
		t.Fatal("expected error from Remove when backend save fails")
	}
}
