package graph

import (
	"encoding/json"
	"testing"
)

// TestFeedEntryHasSixFields verifies that a snapshot JSON-encodes to an object with exactly six keys.
func TestFeedEntryHasSixFields(t *testing.T) {
	snap := NodeSnapshot{
		State:    "running",
		Load:     0.5,
		Slots:    4,
		Memory:   0.62,
		Disk:     0.41,
		Warnings: []string{},
	}
	entry := BuildFeedEntry(snap)
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	expected := map[string]bool{
		"state": true, "load": true, "slots": true,
		"memory": true, "disk": true, "warnings": true,
	}
	if len(m) != len(expected) {
		t.Fatalf("expected %d keys, got %d", len(expected), len(m))
	}
	for k := range expected {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
}

// TestFeedWarningsCappedAtFive verifies that 7 warnings yield only the first 5.
func TestFeedWarningsCappedAtFive(t *testing.T) {
	snap := NodeSnapshot{
		State:    "idle",
		Load:     0.1,
		Slots:    2,
		Memory:   0.3,
		Disk:     0.2,
		Warnings: []string{"w1", "w2", "w3", "w4", "w5", "w6", "w7"},
	}
	entry := BuildFeedEntry(snap)
	if len(entry.Warnings) != 5 {
		t.Fatalf("expected 5 warnings, got %d", len(entry.Warnings))
	}
	for i, w := range entry.Warnings {
		expected := "w" + string(rune('1'+i))
		if w != expected {
			t.Errorf("warning %d: expected %s, got %s", i, expected, w)
		}
	}
}

// TestFeedWarningsEmptyIsArray verifies that zero warnings encode as [] not null.
func TestFeedWarningsEmptyIsArray(t *testing.T) {
	snap := NodeSnapshot{
		State:  "down",
		Load:   0,
		Slots:  0,
		Memory: 0,
		Disk:   0,
	}
	entry := BuildFeedEntry(snap)
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	ws := string(m["warnings"])
	if ws != "[]" {
		t.Errorf("expected [], got %s", ws)
	}
}

// TestFeedMemoryDiskPassThrough verifies memory and disk values pass through unchanged.
func TestFeedMemoryDiskPassThrough(t *testing.T) {
	snap := NodeSnapshot{
		State:    "active",
		Load:     0.9,
		Slots:    8,
		Memory:   0.62,
		Disk:     0.41,
		Warnings: []string{},
	}
	entry := BuildFeedEntry(snap)
	if entry.Memory != 0.62 {
		t.Errorf("expected memory 0.62, got %f", entry.Memory)
	}
	if entry.Disk != 0.41 {
		t.Errorf("expected disk 0.41, got %f", entry.Disk)
	}
}
