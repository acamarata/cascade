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

// TestStoreWaitsKeepSince verifies that SetWait preserves the original since
// timestamp on repeated calls, ClearWait computes elapsed duration and writes
// it to the meta table, and unknown WaitKind values return an error.
func TestStoreWaitsKeepSince(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// SetWait should insert
	err = db.SetWait(WaitingDisk)
	if err != nil {
		t.Fatalf("SetWait failed: %v", err)
	}

	since1 := queryWaitSince(t, db)

	time.Sleep(20 * time.Millisecond)

	// SetWait again should preserve since
	err = db.SetWait(WaitingDisk)
	if err != nil {
		t.Fatalf("SetWait failed: %v", err)
	}

	since2 := queryWaitSince(t, db)

	if !since1.Equal(since2) {
		t.Errorf("SetWait did not preserve since: %v != %v", since1, since2)
	}

	// ClearWait should compute elapsed and write to meta
	err = db.ClearWait(WaitingDisk)
	if err != nil {
		t.Fatalf("ClearWait failed: %v", err)
	}

	assertClearedWait(t, db)
}

// queryWaitSince reads the stored since timestamp for the WaitingDisk row.
func queryWaitSince(t *testing.T, db *DBStore) time.Time {
	t.Helper()

	var since time.Time
	err := db.QueryRow("SELECT since FROM wait WHERE kind=?", int(WaitingDisk)).Scan(&since)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}
	return since
}

// assertClearedWait checks the meta duration, the deleted wait row and the
// errors for unknown wait kinds after ClearWait(WaitingDisk).
func assertClearedWait(t *testing.T, db *DBStore) {
	t.Helper()

	var durStr string
	var err error
	err = db.QueryRow("SELECT value FROM meta WHERE key=?", "last_wait_waiting_disk_duration").Scan(&durStr)
	if err != nil {
		t.Fatalf("QueryRow meta failed: %v", err)
	}

	dur, err := time.ParseDuration(durStr)
	if err != nil {
		t.Errorf("Failed to parse duration from meta: %v", err)
	}
	if dur < 20*time.Millisecond {
		t.Errorf("Expected elapsed duration >= 20ms, got %v", dur)
	}

	// Check wait row is deleted
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM wait WHERE kind=?", int(WaitingDisk)).Scan(&count)
	if err != nil {
		t.Fatalf("QueryRow count failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Wait row not deleted: %d", count)
	}

	// Unknown wait kind should return error
	err = db.SetWait(WaitKind(99))
	if err == nil {
		t.Error("SetWait should return error for unknown WaitKind")
	}
	err = db.ClearWait(WaitKind(99))
	if err == nil {
		t.Error("ClearWait should return error for unknown WaitKind")
	}
}
