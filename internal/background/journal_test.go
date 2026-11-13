package background

import (
	"errors"
	"testing"
)

type testJournal struct {
	appended bool
}

func (j *testJournal) AppendRetry(entry RetryEntry) error {
	j.appended = true
	return nil
}

type testSink struct {
	written bool
}

func (s *testSink) WriteVerdict(entry RetryEntry) error {
	s.written = true
	return nil
}

func TestJournalRejectsEmptyEntry(t *testing.T) {
	cases := []struct {
		name    string
		entry   RetryEntry
		journal RetryJournal
	}{
		{"zero entry", RetryEntry{}, &testJournal{}},
		{"blank ID", RetryEntry{Kind: KindCallFailed, Detail: "d"}, &testJournal{}},
		{"blank Detail", RetryEntry{ID: "id", Kind: KindCallFailed}, &testJournal{}},
		{"unknown Kind", RetryEntry{ID: "id", Kind: "unknown", Detail: "d"}, &testJournal{}},
		{"nil journal", RetryEntry{ID: "id", Kind: KindCallFailed, Detail: "d"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &testSink{}
			err := JournalRetry(tc.entry, tc.journal, sink)
			if !errors.Is(err, ErrInvalidRetryEntry) {
				t.Fatalf("expected ErrInvalidRetryEntry, got %v", err)
			}
			if err.Error() != "D0294" {
				t.Fatalf("expected error message D0294, got %q", err.Error())
			}
			if j, ok := tc.journal.(*testJournal); ok && j.appended {
				t.Fatalf("expected no append, but it was called")
			}
			if sink.written {
				t.Fatalf("expected no verdict, but it was called")
			}
		})
	}
}
