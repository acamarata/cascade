// Package resilience provides types and interfaces for tracking process health.
package resilience

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStoreTypesCompile verifies that all required types, constants, and
// interface methods exist and have the expected signatures.
func TestStoreTypesCompile(_ *testing.T) {
	// WaitKind constants
	_ = WaitingDisk
	_ = WaitingNet
	_ = WaitingModels
	_ = WaitingOwner

	// Struct field checks
	now := time.Now()

	w := Wait{Since: now, Kind: WaitingDisk}
	_ = w.Since
	_ = w.Kind

	r := Restart{Count: 1, Last: now}
	_ = r.Count
	_ = r.Last

	ev := Event{At: now}
	_ = ev.At

	// Store interface compile check via nil pointer
	var st Store
	if st != nil {
		_ = st.SetWait(WaitingDisk)
		_ = st.ClearWait(WaitingNet)
		_ = st.RecordHeartbeat()
		_ = st.MarkSeenRunning()
		_ = st.NextRestartDelay()
		_ = st.RecordStall("test")
		_ = st.EmitEvent(ev)
		_ = st.TrimEvents()
	}
}

// TestOpenCreatesTables verifies that Open creates a SQLite database
// with WAL mode, synchronous=FULL, and all required tables.
func TestOpenCreatesTables(t *testing.T) {
	// Test empty path returns error
	_, err := Open("")
	if err == nil {
		t.Fatal("Open with empty path should return error")
	}

	// Create temp directory for test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Open database
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify it's a valid DBStore
	if db == nil {
		t.Fatal("Open returned nil DBStore")
	}

	// Verify DBStore satisfies Store interface
	var _ Store = db

	// Check that all required tables exist
	requiredTables := []string{"meta", "wait", "heartbeat", "seen_running", "restart", "stall", "event"}
	for _, table := range requiredTables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("Table %s does not exist: %v", table, err)
		}
	}

	// Verify WAL mode
	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("Failed to query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("Expected journal_mode=wal, got %s", journalMode)
	}

	// Verify synchronous=FULL
	var synchronous string
	err = db.QueryRow("PRAGMA synchronous").Scan(&synchronous)
	if err != nil {
		t.Fatalf("Failed to query synchronous: %v", err)
	}
	// synchronous can be returned as "2" (FULL) or "full"
	if synchronous != "2" && synchronous != "full" {
		t.Errorf("Expected synchronous=FULL (2), got %s", synchronous)
	}

	// Verify the database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}
