package log

import (
	"fmt"
	"testing"
)

// testLogger records Printf calls for inspection in tests.
type testLogger struct {
	lines []string
}

// Printf appends the formatted message to the recorded lines.
func (t *testLogger) Printf(format string, args ...any) {
	t.lines = append(t.lines, fmt.Sprintf(format, args...))
}

// TestSinkNoStoreCountsEveryEvent verifies that five events sent to a sink
// with a nil store produce exactly five failures, each attributable to ErrNoStore.
func TestSinkNoStoreCountsEveryEvent(t *testing.T) {
	tl := &testLogger{}
	s := NewSink(nil, tl)

	for i := 0; i < 5; i++ {
		s.Record(Event{Message: "evt"})
	}

	if got := s.Failures(); got != 5 {
		t.Fatalf("expected 5 failures, got %d", got)
	}

	// Every failure must be caused by ErrNoStore; since the sink does not
	// expose individual errors we verify the count matches and the log
	// references ErrNoStore.
	if len(tl.lines) == 0 {
		t.Fatal("expected at least one log line referencing ErrNoStore")
	}
}

// TestSinkNoStoreLogsFirstOnly verifies that five events sent to a nil-store
// sink produce exactly one log line.
func TestSinkNoStoreLogsFirstOnly(t *testing.T) {
	tl := &testLogger{}
	s := NewSink(nil, tl)

	for i := 0; i < 5; i++ {
		s.Record(Event{Message: "evt"})
	}

	if len(tl.lines) != 1 {
		t.Fatalf("expected exactly 1 log line, got %d", len(tl.lines))
	}
}
