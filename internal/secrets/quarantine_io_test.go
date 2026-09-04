package secrets

// Purpose: the quarantine store's I/O failure paths. A ledger that cannot
//
//	be written or read must SAY so: silently behaving like an empty store
//	would hide both the findings and the fact that they were lost.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestQuarantineReportsIOFailures: when the ledger path is not a writable
// file, both the write and the read path must report it rather than
// behaving like an empty store - a quarantine that silently loses records
// is worse than one that refuses.
func TestQuarantineReportsIOFailures(t *testing.T) {
	dir := t.TempDir()
	store, err := NewQuarantineStore(dir, fixedClock{at: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, quarantineLogName), 0o700); err != nil {
		t.Fatalf("blocking the ledger path: %v", err)
	}
	if _, err := store.Put(classHit(ClassAPIKey, 1), "ref", nil); err == nil {
		t.Error("a write to an unwritable ledger reported success")
	}
	if _, err := store.List(); err == nil {
		t.Error("a read of an unreadable ledger reported an empty store")
	}
	if _, err := store.Get("any"); err == nil {
		t.Error("Get over an unreadable ledger reported not-found")
	}
	if err := store.Delete("any", ReleasePromoted); err == nil {
		t.Error("Delete over an unreadable ledger reported not-found")
	}
}

// TestQuarantineRefusesAnUncreatableDirectory covers the constructor's
// other failure: a path that cannot become a directory.
func TestQuarantineRefusesAnUncreatableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := NewQuarantineStore(filepath.Join(file, "under"), fixedClock{}); err == nil {
		t.Fatal("a directory under a regular file was accepted")
	}
}
