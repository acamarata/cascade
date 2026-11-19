package write

import "testing"

// TestWriteScheduledIndexEventEmptyNames verifies that nil and empty Names
// are normalized to a nonnil empty slice and that exactly one row is written.
func TestWriteScheduledIndexEventEmptyNames(t *testing.T) {
	tests := []struct {
		name  string
		event ScheduledIndexEvent
	}{
		{
			name:  "nil names",
			event: ScheduledIndexEvent{Counts: 0, Names: nil},
		},
		{
			name:  "empty names",
			event: ScheduledIndexEvent{Counts: 0, Names: []string{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &mockEventSink{}
			err := WriteScheduledIndexEvent(sink, tt.event)
			if err != nil {
				t.Errorf("WriteScheduledIndexEvent() error = %v, want nil", err)
			}
			if len(sink.calls) != 1 {
				t.Errorf("WriteScheduledIndexEvent() wrote %d events, want 1", len(sink.calls))
			}
			if len(sink.calls) == 1 {
				if sink.calls[0].Names == nil {
					t.Errorf("WriteScheduledIndexEvent() Names is nil, want nonnil empty slice")
				}
				if len(sink.calls[0].Names) != 0 {
					t.Errorf("WriteScheduledIndexEvent() Names length = %d, want 0", len(sink.calls[0].Names))
				}
			}
		})
	}
}
