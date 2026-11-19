package background

import (
	"testing"
)

func TestEmptyBatchNoQuotaChange(t *testing.T) {
	q := &Quota{PerHour: 5}

	// Test nil events
	entries, err := ProcessBatch(nil, q)
	if err != nil {
		t.Fatalf("expected no error for nil events, got %v", err)
	}
	if entries == nil {
		t.Fatalf("expected non-nil empty entries for nil events")
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
	if q.PerHour != 5 {
		t.Fatalf("expected quota to remain 5, got %d", q.PerHour)
	}

	// Test empty slice
	entries, err = ProcessBatch([]BatchEvent{}, q)
	if err != nil {
		t.Fatalf("expected no error for empty events, got %v", err)
	}
	if entries == nil {
		t.Fatalf("expected non-nil empty entries for empty events")
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
	if q.PerHour != 5 {
		t.Fatalf("expected quota to remain 5, got %d", q.PerHour)
	}
}

func TestLeadingEventNotCovered(t *testing.T) {
	q := &Quota{PerHour: 10}

	events := []BatchEvent{
		{ID: "evt-1", Leading: true},
		{ID: "evt-2", Leading: false},
		{ID: "evt-3", Leading: true},
	}

	entries, err := ProcessBatch(events, q)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// evt-1 is leading, so Covered should be false
	if entries[0].ID != "evt-1" || entries[0].Covered != false {
		t.Fatalf("expected evt-1 Covered=false, got %+v", entries[0])
	}

	// evt-2 is not leading, so Covered should be true
	if entries[1].ID != "evt-2" || entries[1].Covered != true {
		t.Fatalf("expected evt-2 Covered=true, got %+v", entries[1])
	}

	// evt-3 is leading, so Covered should be false
	if entries[2].ID != "evt-3" || entries[2].Covered != false {
		t.Fatalf("expected evt-3 Covered=false, got %+v", entries[2])
	}

	// Quota should increment by 2 (two leading events)
	if q.PerHour != 12 {
		t.Fatalf("expected quota to be 12, got %d", q.PerHour)
	}
}
