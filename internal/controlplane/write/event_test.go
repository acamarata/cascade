package write

import (
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

func TestWriteScheduledIndexEventValid(t *testing.T) {
	sink := &mockEventSink{}
	event := ScheduledIndexEvent{
		Counts: 3,
		Names:  []string{"a", "b", "c"},
	}

	err := WriteScheduledIndexEvent(sink, event)
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
	err = WriteScheduledIndexEvent(sink, event)
	if err != expectedErr {
		t.Errorf("expected exact error %v, got %v", expectedErr, err)
	}
	if len(sink.calls) != 1 {
		t.Errorf("expected exactly 1 call without retry, got %d", len(sink.calls))
	}
}
