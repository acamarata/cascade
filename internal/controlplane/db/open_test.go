package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSetsWALAndBusyTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("", "dbtest-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("failed to remove temp dir: %v", err)
		}
	}()

	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close db: %v", err)
		}
	}()

	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("failed to query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", journalMode)
	}

	var busyTimeout int
	err = db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)
	if err != nil {
		t.Fatalf("failed to query busy_timeout: %v", err)
	}
	if busyTimeout != 3000 {
		t.Errorf("expected busy_timeout=3000, got %d", busyTimeout)
	}

	// Test invalid path
	_, err = Open("/invalid/path/that/does/not/exist/test.db")
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}
