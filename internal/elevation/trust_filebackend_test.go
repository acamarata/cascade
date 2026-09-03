// Purpose: fileBackend-specific tests, split out of trust_test.go purely
//
//	to respect the 300-line file cap (Art.10.3) — these tests exercise
//	the real on-disk persistence (t.TempDir()-rooted, Art.7.1: never the
//	real CASCADE_HOME) rather than memBackend's in-memory fake.
//
// SPORT: internal/elevation fileBackend tests/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileBackend_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileBackend(dir)
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))

	if store.IsEnrolled() {
		t.Fatal("fresh fileBackend reports enrolled")
	}
	fp, err := store.Enroll(pubKeyA)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// A second store instance over the same directory must see the same
	// record (proves persistence, not just in-process state).
	store2 := NewElevationTrustStore(NewFileBackend(dir), fixedClock(time.Unix(2000, 0)))
	if !store2.Verify(fp) {
		t.Fatal("a second ElevationTrustStore over the same dir did not see the persisted record")
	}
	if _, err := store2.Enroll(pubKeyB); err == nil {
		t.Fatal("a second store instance's Enroll must still be refused by the persisted record")
	}
}

func TestFileBackend_Load_GenericIOError(t *testing.T) {
	// A path whose parent component is a FILE, not a directory, makes
	// os.ReadFile fail with something other than os.IsNotExist — exercises
	// fileBackend.Load's generic-error branch (distinct from "not found").
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	backend := NewFileBackend(blocker).(fileBackend) // dataDir/elevation/trust.json under a FILE
	_, _, err := backend.Load()
	if err == nil {
		t.Fatal("Load must surface a generic I/O error, not silently report absent")
	}
}

func TestFileBackend_Save_WriteFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	backend := NewFileBackend(blocker).(fileBackend)
	err := backend.Save(TrustRecord{PubKeyB64: pubKeyA})
	if err == nil {
		t.Fatal("Save must surface a write failure when its parent path is not a directory")
	}
}

func TestFileBackend_CorruptFile_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileBackend(dir).(fileBackend)
	if err := writeCorruptFile(backend.path); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}
	_, ok, err := backend.Load()
	if err == nil {
		t.Fatal("Load of a corrupt file must return an error")
	}
	if ok {
		t.Fatal("Load of a corrupt file must not report ok=true")
	}
}
