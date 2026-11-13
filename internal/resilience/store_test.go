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

	var since1 time.Time
	err = db.QueryRow("SELECT since FROM wait WHERE kind=?", int(WaitingDisk)).Scan(&since1)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	// SetWait again should preserve since
	err = db.SetWait(WaitingDisk)
	if err != nil {
		t.Fatalf("SetWait failed: %v", err)
	}

	var since2 time.Time
	err = db.QueryRow("SELECT since FROM wait WHERE kind=?", int(WaitingDisk)).Scan(&since2)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}

	if !since1.Equal(since2) {
		t.Errorf("SetWait did not preserve since: %v != %v", since1, since2)
	}

	// ClearWait should compute elapsed and write to meta
	err = db.ClearWait(WaitingDisk)
	if err != nil {
		t.Fatalf("ClearWait failed: %v", err)
	}

	var durStr string
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

// TestRestartBackoff verifies that NextRestartDelay returns an exponential
// backoff sequence capped at 30 minutes, increments the count, and updates
// the last restart timestamp.
func TestRestartBackoff(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	expected := []time.Duration{
		1 * time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		30 * time.Minute,
	}

	for i, want := range expected {
		got := db.NextRestartDelay()
		if got != want {
			t.Errorf("Call %d: expected %v, got %v", i+1, want, got)
		}
	}

	// Verify count persists and sequence continues capping at 30
	got := db.NextRestartDelay()
	if got != 30*time.Minute {
		t.Errorf("Call after max: expected 30m, got %v", got)
	}

	// Verify last timestamp was updated
	var last time.Time
	err = db.QueryRow("SELECT last FROM restart LIMIT 1").Scan(&last)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}
	if time.Since(last) > time.Minute {
		t.Errorf("Last restart timestamp not updated recently: %v", last)
	}

	// Verify count
	var count int
	err = db.QueryRow("SELECT count FROM restart LIMIT 1").Scan(&count)
	if err != nil {
		t.Fatalf("QueryRow count failed: %v", err)
	}
	if count != 7 {
		t.Errorf("Expected count 7, got %d", count)
	}
}

// TestEventsTrim verifies that EmitEvent inserts events, TrimEvents deletes
// events older than 14 days while preserving recent ones, RecordHeartbeat
// updates the heartbeat timestamp, MarkSeenRunning inserts a row, and
// RecordStall inserts a stall record.
func TestEventsTrim(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert an old event (20 days ago)
	oldTime := time.Now().UTC().Add(-20 * 24 * time.Hour)
	err = db.EmitEvent(Event{At: oldTime})
	if err != nil {
		t.Fatalf("EmitEvent old failed: %v", err)
	}

	// Insert a recent event (now)
	recentTime := time.Now().UTC()
	err = db.EmitEvent(Event{At: recentTime})
	if err != nil {
		t.Fatalf("EmitEvent recent failed: %v", err)
	}

	// Verify both events exist
	var countBefore int
	err = db.QueryRow("SELECT COUNT(*) FROM event").Scan(&countBefore)
	if err != nil {
		t.Fatalf("Count before trim failed: %v", err)
	}
	if countBefore != 2 {
		t.Fatalf("Expected 2 events before trim, got %d", countBefore)
	}

	// TrimEvents should delete old events
	err = db.TrimEvents()
	if err != nil {
		t.Fatalf("TrimEvents failed: %v", err)
	}

	// Verify only the recent event remains
	var countAfter int
	err = db.QueryRow("SELECT COUNT(*) FROM event").Scan(&countAfter)
	if err != nil {
		t.Fatalf("Count after trim failed: %v", err)
	}
	if countAfter != 1 {
		t.Errorf("Expected 1 event after trim, got %d", countAfter)
	}

	// Verify the remaining event is the recent one
	var at time.Time
	err = db.QueryRow("SELECT at FROM event LIMIT 1").Scan(&at)
	if err != nil {
		t.Fatalf("Query event at failed: %v", err)
	}
	if !at.Equal(recentTime) {
		t.Errorf("Remaining event time mismatch: got %v, want %v", at, recentTime)
	}

	// EmitEvent with zero time should use current UTC time
	err = db.EmitEvent(Event{})
	if err != nil {
		t.Fatalf("EmitEvent zero time failed: %v", err)
	}

	// RecordHeartbeat should update the heartbeat table
	err = db.RecordHeartbeat()
	if err != nil {
		t.Fatalf("RecordHeartbeat failed: %v", err)
	}
	var hbAt time.Time
	err = db.QueryRow("SELECT at FROM heartbeat LIMIT 1").Scan(&hbAt)
	if err != nil {
		t.Fatalf("Query heartbeat failed: %v", err)
	}
	if time.Since(hbAt) > time.Minute {
		t.Errorf("Heartbeat timestamp too old: %v", hbAt)
	}

	// MarkSeenRunning should insert into seen_running
	err = db.MarkSeenRunning()
	if err != nil {
		t.Fatalf("MarkSeenRunning failed: %v", err)
	}
	var srCount int
	err = db.QueryRow("SELECT COUNT(*) FROM seen_running").Scan(&srCount)
	if err != nil {
		t.Fatalf("Query seen_running count failed: %v", err)
	}
	if srCount < 1 {
		t.Errorf("Expected at least 1 seen_running row, got %d", srCount)
	}

	// RecordStall should insert into stall
	err = db.RecordStall("disk full")
	if err != nil {
		t.Fatalf("RecordStall failed: %v", err)
	}
	var stallReason string
	err = db.QueryRow("SELECT reason FROM stall LIMIT 1").Scan(&stallReason)
	if err != nil {
		t.Fatalf("Query stall failed: %v", err)
	}
	if stallReason != "disk full" {
		t.Errorf("Expected stall reason 'disk full', got '%s'", stallReason)
	}
}
