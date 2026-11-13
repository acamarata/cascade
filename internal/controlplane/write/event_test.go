package write

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type mockEventSink struct {
	calls []ScheduledIndexEvent
	err   error
}

func (m *mockEventSink) InsertScheduledIndexEvent(event ScheduledIndexEvent) error {
	// Copy names to record what was passed before mutation
	recordedNames := make([]string, len(event.Names))
	copy(recordedNames, event.Names)

	m.calls = append(m.calls, ScheduledIndexEvent{
		Counts: event.Counts,
		Names:  recordedNames,
	})

	if len(event.Names) > 0 {
		event.Names[0] = "mutated"
	}
	return m.err
}

func TestEmitScheduledIndexEventValid(t *testing.T) {
	sink := &mockEventSink{}
	event := ScheduledIndexEvent{
		Counts: 3,
		Names:  []string{"a", "b", "c"},
	}

	err := EmitScheduledIndexEvent(sink, event)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if len(sink.calls) != 1 {
		t.Fatalf("expected exactly 1 sink call, got %d", len(sink.calls))
	}

	if sink.calls[0].Counts != 3 {
		t.Errorf("expected Counts 3, got %d", sink.calls[0].Counts)
	}

	expectedNames := []string{"a", "b", "c"}
	if !reflect.DeepEqual(sink.calls[0].Names, expectedNames) {
		t.Errorf("expected Names %v, got %v", expectedNames, sink.calls[0].Names)
	}

	if event.Names[0] != "a" {
		t.Errorf("input was mutated: %v", event.Names)
	}

	expectedErr := errors.New("sink error")
	sink.err = expectedErr
	sink.calls = nil
	err = EmitScheduledIndexEvent(sink, event)
	if err != expectedErr {
		t.Errorf("expected exact error %v, got %v", expectedErr, err)
	}
	if len(sink.calls) != 1 {
		t.Errorf("expected exactly 1 call without retry, got %d", len(sink.calls))
	}
}

// TestWriteScheduledIndexEventSchemaExcludesText verifies via reflection and
// JSON serialization that ScheduledIndexEvent contains only Counts and Names,
// with no Text, Body, Content, or arbitrary metadata fields.
func TestWriteScheduledIndexEventSchemaExcludesText(t *testing.T) {
	val := reflect.ValueOf(ScheduledIndexEvent{})
	typ := val.Type()
	if typ.NumField() != 2 {
		t.Fatalf("expected exactly 2 exported fields, got %d", typ.NumField())
	}

	f0 := typ.Field(0)
	if f0.Name != "Counts" || f0.Type.Kind() != reflect.Int {
		t.Errorf("expected field 0 Counts int, got %s %s", f0.Name, f0.Type.Kind())
	}

	f1 := typ.Field(1)
	if f1.Name != "Names" || f1.Type.Kind() != reflect.Slice || f1.Type.Elem().Kind() != reflect.String {
		t.Errorf("expected field 1 Names []string, got %s %s", f1.Name, f1.Type.Kind())
	}

	event := ScheduledIndexEvent{Counts: 1, Names: []string{"x"}}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if len(m) != 2 {
		t.Errorf("expected exactly 2 keys in serialized output, got %d: %v", len(m), m)
	}

	if _, ok := m["Counts"]; !ok {
		t.Errorf("missing Counts key in serialized output")
	}
	if _, ok := m["Names"]; !ok {
		t.Errorf("missing Names key in serialized output")
	}

	forbidden := []string{"Text", "Body", "Content", "Metadata", "text", "body", "content", "metadata"}
	for _, k := range forbidden {
		if _, ok := m[k]; ok {
			t.Errorf("forbidden key %q found in serialized output", k)
		}
	}
}
