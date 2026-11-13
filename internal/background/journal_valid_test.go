package background

import (
	"errors"
	"testing"
)

type validTestJournal struct {
	entries []RetryEntry
	err     error
}

func (j *validTestJournal) AppendRetry(entry RetryEntry) error {
	if j.err != nil {
		return j.err
	}
	j.entries = append(j.entries, entry)
	return nil
}

type validTestSink struct {
	called bool
}

func (s *validTestSink) WriteVerdict(entry RetryEntry) error {
	s.called = true
	return nil
}

// TestJournalFailedCall verifies valid call failure routing.
func TestJournalFailedCall(t *testing.T) {
	j := &validTestJournal{}
	s := &validTestSink{}
	entry := RetryEntry{ID: "c1", Kind: KindCallFailed, Detail: "failed"}

	err := JournalRetry(entry, j, s)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(j.entries) != 1 || j.entries[0] != entry {
		t.Fatalf("expected exact entry once, got %v", j.entries)
	}
	if s.called {
		t.Fatalf("verdict must not be invoked")
	}

	j.err = ErrDuplicateRetryEntry
	err = JournalRetry(entry, j, s)
	if !errors.Is(err, ErrRetryJournal) {
		t.Errorf("expected ErrRetryJournal, got %v", err)
	}
	if !errors.Is(err, ErrDuplicateRetryEntry) {
		t.Errorf("expected ErrDuplicateRetryEntry, got %v", err)
	}
	if len(j.entries) != 1 {
		t.Errorf("expected no extra entry, got %d", len(j.entries))
	}
}

// TestJournalOpenerFailure verifies opener failure routing.
func TestJournalOpenerFailure(t *testing.T) {
	j := &validTestJournal{}
	s := &validTestSink{}
	entry := RetryEntry{ID: "o1", Kind: KindOpenerFailed, Detail: "opener"}

	err := JournalRetry(entry, j, s)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(j.entries) != 1 || j.entries[0] != entry {
		t.Fatalf("expected exact entry once, got %v", j.entries)
	}
	if s.called {
		t.Fatalf("verdict must not be invoked")
	}
}

// TestJournalHourlyCapRefusal verifies hourly cap refusal routing.
func TestJournalHourlyCapRefusal(t *testing.T) {
	j := &validTestJournal{}
	s := &validTestSink{}
	entry := RetryEntry{ID: "h1", Kind: KindHourlyCap, Detail: "cap"}

	err := JournalRetry(entry, j, s)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(j.entries) != 1 || j.entries[0] != entry {
		t.Fatalf("expected exact entry once, got %v", j.entries)
	}
	if s.called {
		t.Fatalf("verdict must not be invoked")
	}
}
