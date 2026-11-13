package consult

import (
	"errors"
	"testing"
)

// TestPreferenceSurvivesReload verifies that routing preferences survive
// a marshal/unmarshal cycle, maintaining independence from the original.
func TestPreferenceSurvivesReload(t *testing.T) {
	m := NewContextMemory()
	if err := m.SaveRoutingPreference("ctx-2", RoutingPreference{Class: "premium", Model: "gpt-4"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveRoutingPreference("ctx-1", RoutingPreference{Class: "standard", Model: "gpt-3.5"}); err != nil {
		t.Fatal(err)
	}

	data, err := m.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Verify sorted order: ctx-1 before ctx-2
	expected := `[{"context_id":"ctx-1","class":"standard","model":"gpt-3.5"},{"context_id":"ctx-2","class":"premium","model":"gpt-4"}]`
	if string(data) != expected {
		t.Errorf("Marshal output mismatch:\ngot:  %s\nwant: %s", string(data), expected)
	}

	// Verify no trailing newline
	if len(data) > 0 && data[len(data)-1] == '\n' {
		t.Error("Marshal output should not have trailing newline")
	}

	reloaded, err := UnmarshalContextMemory(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify preferences survived
	p1, err := reloaded.LoadRoutingPreference("ctx-1")
	if err != nil {
		t.Fatalf("LoadRoutingPreference failed for ctx-1: %v", err)
	}
	if p1.Class != "standard" || p1.Model != "gpt-3.5" {
		t.Errorf("ctx-1 preference mismatch: got %+v", p1)
	}

	p2, err := reloaded.LoadRoutingPreference("ctx-2")
	if err != nil {
		t.Fatalf("LoadRoutingPreference failed for ctx-2: %v", err)
	}
	if p2.Class != "premium" || p2.Model != "gpt-4" {
		t.Errorf("ctx-2 preference mismatch: got %+v", p2)
	}
}

// TestReloadedMemoryIndependentFromOriginal verifies that modifying the
// original after a reload leaves the reloaded memory unchanged.
func TestReloadedMemoryIndependentFromOriginal(t *testing.T) {
	m := NewContextMemory()
	if err := m.SaveRoutingPreference("ctx-1", RoutingPreference{Class: "standard", Model: "gpt-3.5"}); err != nil {
		t.Fatal(err)
	}
	data, err := m.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	reloaded, err := UnmarshalContextMemory(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify independence: modify original, reloaded should be unchanged
	if err := m.SaveRoutingPreference("ctx-1", RoutingPreference{Class: "modified", Model: "modified"}); err != nil {
		t.Fatal(err)
	}
	p1After, err := reloaded.LoadRoutingPreference("ctx-1")
	if err != nil {
		t.Fatal(err)
	}
	if p1After.Class != "standard" {
		t.Error("Reloaded memory is not independent from original")
	}
}

// TestMarshalEmptyStateEmitsBrackets verifies that an empty state marshals to [].
func TestMarshalEmptyStateEmitsBrackets(t *testing.T) {
	m := NewContextMemory()
	data, err := m.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("empty Marshal should emit [], got %s", string(data))
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		t.Error("empty Marshal should not have trailing newline")
	}
}

// TestMarshalNilReceiver verifies that Marshal on a nil receiver fails.
func TestMarshalNilReceiver(t *testing.T) {
	var m *ContextMemory
	data, err := m.Marshal()
	if data != nil {
		t.Errorf("nil Marshal should return nil data, got %v", data)
	}
	if !errors.Is(err, ErrInvalidContextMemory) {
		t.Errorf("nil Marshal should return ErrInvalidContextMemory, got %v", err)
	}
}

// TestUnmarshalRejectsInvalidInputs verifies that invalid inputs are rejected.
func TestUnmarshalRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"nil bytes", nil},
		{"empty bytes", []byte{}},
		{"null", []byte("null")},
		{"malformed JSON", []byte("{not json")},
		{"trailing data", []byte(`[{"context_id":"ctx-1","class":"a","model":"b"}]extra`)},
		{"blank context_id", []byte(`[{"context_id":"","class":"a","model":"b"}]`)},
		{"blank class", []byte(`[{"context_id":"ctx-1","class":"","model":"b"}]`)},
		{"blank model", []byte(`[{"context_id":"ctx-1","class":"a","model":""}]`)},
		{"missing context_id", []byte(`[{"class":"a","model":"b"}]`)},
		{"missing class", []byte(`[{"context_id":"ctx-1","model":"b"}]`)},
		{"missing model", []byte(`[{"context_id":"ctx-1","class":"a"}]`)},
		{"unknown key", []byte(`[{"context_id":"ctx-1","class":"a","model":"b","extra":"c"}]`)},
		{"duplicate context_id", []byte(`[{"context_id":"ctx-1","class":"a","model":"b"},{"context_id":"ctx-1","class":"c","model":"d"}]`)},
		{"duplicate key in record", []byte(`[{"context_id":"ctx-1","class":"a","model":"b","context_id":"ctx-2"}]`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := UnmarshalContextMemory(tc.data)
			if m != nil {
				t.Errorf("Unmarshal should return nil for invalid input, got %v", m)
			}
			if !errors.Is(err, ErrInvalidContextMemory) {
				t.Errorf("Unmarshal should return ErrInvalidContextMemory, got %v", err)
			}
		})
	}
}

// TestUnmarshalAcceptsVariedKeyOrder verifies that key order may vary.
func TestUnmarshalAcceptsVariedKeyOrder(t *testing.T) {
	// Keys in different order should still parse
	data := []byte(`[{"model":"gpt-4","class":"premium","context_id":"ctx-1"}]`)
	m, err := UnmarshalContextMemory(data)
	if err != nil {
		t.Fatalf("Unmarshal failed with varied key order: %v", err)
	}
	p, err := m.LoadRoutingPreference("ctx-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != "premium" || p.Model != "gpt-4" {
		t.Errorf("preference mismatch: got %+v", p)
	}
}

// TestUnmarshalAcceptsVariedRecordOrder verifies that record order may vary.
func TestUnmarshalAcceptsVariedRecordOrder(t *testing.T) {
	// Records in different order should still parse (but output is always sorted)
	data := []byte(`[{"context_id":"ctx-2","class":"b","model":"B"},{"context_id":"ctx-1","class":"a","model":"A"}]`)
	m, err := UnmarshalContextMemory(data)
	if err != nil {
		t.Fatalf("Unmarshal failed with varied record order: %v", err)
	}
	// Verify both preferences loaded
	p1, err := m.LoadRoutingPreference("ctx-1")
	if err != nil {
		t.Fatal(err)
	}
	if p1.Class != "a" {
		t.Errorf("ctx-1 preference mismatch: got %+v", p1)
	}
	p2, err := m.LoadRoutingPreference("ctx-2")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Class != "b" {
		t.Errorf("ctx-2 preference mismatch: got %+v", p2)
	}
}
