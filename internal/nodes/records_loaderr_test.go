package nodes

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRecordStoreList_BackendLoadErrorPropagates: List's own backend.Load
// call site is a separate statement from Enroll's (records_test.go's
// TestRecordStoreBackendUnavailable only exercises Enroll's), so it needs
// its own coverage.
func TestRecordStoreList_BackendLoadErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated storage outage")
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	if _, err := store.List(); err == nil {
		t.Fatal("expected error when backend load fails")
	}
}

// TestRecordStoreRemove_BackendLoadErrorPropagates: Remove's own Load
// call site, distinct from Enroll's and List's.
func TestRecordStoreRemove_BackendLoadErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "loaderr-remove")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated storage outage")
	if err := store.Remove(id.NodeID); err == nil {
		t.Fatal("expected error when backend load fails")
	}
}

// TestRecordStorePut_BackendLoadErrorPropagates: put's own Load call
// site, distinct from the others above.
func TestRecordStorePut_BackendLoadErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "loaderr-put")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated storage outage")
	if err := store.put(rec); err == nil {
		t.Fatal("expected error when backend load fails")
	}
}

// TestFileRecordBackendLoad_UnreadableNotNotExist proves fileRecordBackend
// distinguishes "does not exist" (a valid empty-store state) from a real
// read failure: a directory sitting at the expected file path makes
// os.ReadFile fail with something other than IsNotExist, which must
// propagate rather than being swallowed into an empty map.
func TestFileRecordBackendLoad_UnreadableNotNotExist(t *testing.T) {
	dir := t.TempDir()
	nodesDir := filepath.Join(dir, "nodes")
	// A directory at the exact file path: os.ReadFile refuses with
	// EISDIR, which is not os.IsNotExist.
	if err := os.MkdirAll(filepath.Join(nodesDir, "devices.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := NewFileRecordBackend(dir)
	if _, err := backend.Load(); err == nil {
		t.Fatal("expected a read error for an unreadable (directory) store path")
	}
}

// TestFileRecordBackendSave_MkdirFails proves Save's atomic-write
// failure propagates: a plain FILE sitting where the "nodes" directory
// must be created makes os.MkdirAll fail.
func TestFileRecordBackendSave_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "nodes")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := NewFileRecordBackend(dir)
	if err := backend.Save(map[string]DeviceRecord{}); err == nil {
		t.Fatal("expected Save to fail when its directory path is blocked by a file")
	}
}
