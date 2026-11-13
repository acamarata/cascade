package write

import (
	"testing"
	"time"
)

func TestOwnerRestartRecorded(t *testing.T) {
	var store AuditStore
	at, _ := time.Parse(time.RFC3339, "2026-11-05T10:00:00Z")

	err := RecordOwnerRestart(&store, "T-1", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	ev := store.events[0]
	if ev.Kind != "owner_restart" {
		t.Errorf("expected kind owner_restart, got %q", ev.Kind)
	}
	if ev.Item != "T-1" {
		t.Errorf("expected item T-1, got %q", ev.Item)
	}
	if !ev.At.Equal(at) {
		t.Errorf("expected at %v, got %v", at, ev.At)
	}
	if ev.At.Location() != time.UTC {
		t.Errorf("expected UTC location, got %v", ev.At.Location())
	}

	// non-UTC timestamp converts to UTC preserving instant/nanoseconds
	loc := time.FixedZone("TEST", 3600)
	at2 := time.Date(2026, 11, 5, 11, 0, 0, 123456789, loc)
	err = RecordOwnerRestart(&store, "T-2", at2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(store.events))
	}
	ev2 := store.events[1]
	if ev2.Kind != "owner_restart" {
		t.Errorf("expected kind owner_restart, got %q", ev2.Kind)
	}
	if ev2.Item != "T-2" {
		t.Errorf("expected item T-2, got %q", ev2.Item)
	}
	if !ev2.At.Equal(at2) {
		t.Errorf("expected at to be equal to %v, got %v", at2, ev2.At)
	}
	if ev2.At.Location() != time.UTC {
		t.Errorf("expected UTC location, got %v", ev2.At.Location())
	}
}
