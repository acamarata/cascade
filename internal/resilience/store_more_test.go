package resilience

import (
	"path/filepath"
	"testing"
	"time"
)

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
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	}()

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

// openEventsTestDB opens a fresh store in a temp dir and closes it on cleanup.
func openEventsTestDB(t *testing.T) *DBStore {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEventsTrim verifies that EmitEvent inserts events and TrimEvents deletes
// events older than 14 days while preserving recent ones.
func TestEventsTrim(t *testing.T) {
	db := openEventsTestDB(t)

	// Insert an old event (20 days ago)
	oldTime := time.Now().UTC().Add(-20 * 24 * time.Hour)
	err := db.EmitEvent(Event{At: oldTime})
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
}

// TestEventZeroTimeAndHeartbeat verifies that EmitEvent accepts a zero time
// and that RecordHeartbeat updates the heartbeat timestamp.
func TestEventZeroTimeAndHeartbeat(t *testing.T) {
	db := openEventsTestDB(t)

	// EmitEvent with zero time should use current UTC time
	err := db.EmitEvent(Event{})
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
}

// TestSeenRunningAndStall verifies that MarkSeenRunning inserts a row and
// RecordStall inserts a stall record.
func TestSeenRunningAndStall(t *testing.T) {
	db := openEventsTestDB(t)

	// MarkSeenRunning should insert into seen_running
	err := db.MarkSeenRunning()
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
