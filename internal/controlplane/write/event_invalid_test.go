package write

import (
	"errors"
	"testing"
)

type invalidMockEventSink struct {
	calls int
}

func (m *invalidMockEventSink) InsertScheduledIndexEvent(event ScheduledIndexEvent) error {
	m.calls++
	return nil
}

func TestWriteScheduledIndexEventNegativeCount(t *testing.T) {
	cases := []struct {
		name  string
		sink  EventSink
		event ScheduledIndexEvent
	}{
		{
			name:  "nil sink",
			sink:  nil,
			event: ScheduledIndexEvent{Counts: 0, Names: []string{}},
		},
		{
			name:  "negative count",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: -1, Names: []string{}},
		},
		{
			name:  "mismatched count and names length",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: 2, Names: []string{"a"}},
		},
		{
			name:  "blank name",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: 1, Names: []string{""}},
		},
		{
			name:  "whitespace only name",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: 1, Names: []string{"   "}},
		},
		{
			name:  "leading whitespace",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: 1, Names: []string{" a"}},
		},
		{
			name:  "trailing whitespace",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: 1, Names: []string{"a "}},
		},
		{
			name:  "duplicate exact name",
			sink:  &invalidMockEventSink{},
			event: ScheduledIndexEvent{Counts: 2, Names: []string{"a", "a"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteScheduledIndexEvent(tc.sink, tc.event)
			if !errors.Is(err, ErrInvalidScheduledIndexEvent) {
				t.Errorf("expected ErrInvalidScheduledIndexEvent, got %v", err)
			}
			if ms, ok := tc.sink.(*invalidMockEventSink); ok && ms.calls > 0 {
				t.Errorf("expected zero calls to sink, got %d", ms.calls)
			}
		})
	}
}
