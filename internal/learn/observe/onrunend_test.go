package observe

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStore struct {
	runs        map[string]bool
	commitCalls atomic.Int32
}

func newFakeStore() *fakeStore {
	return &fakeStore{runs: make(map[string]bool)}
}

func (s *fakeStore) HasRun(id string) (bool, error) {
	return s.runs[id], nil
}

func (s *fakeStore) HasRecommendation(day string) (bool, error) {
	return false, nil
}

func (s *fakeStore) Commit(run Run, day string, rec Rec, hasRec bool) error {
	s.commitCalls.Add(1)
	s.runs[run.ID] = true
	return nil
}

type fakeDecider struct {
	called atomic.Bool
}

func (d *fakeDecider) Decide(run Run, domain string) (Rec, bool, error) {
	d.called.Store(true)
	return Rec{RunID: run.ID, Domain: domain, Text: "rec"}, true, nil
}

type fakeInbox struct {
	called atomic.Bool
}

func (i *fakeInbox) Notify(rec Rec) error {
	i.called.Store(true)
	return nil
}

// TestRecordsCodeRun verifies that a valid code run is recorded exactly once.
func TestRecordsCodeRun(t *testing.T) {
	store := newFakeStore()
	decider := &fakeDecider{}
	inbox := &fakeInbox{}
	hook := EndHook{Store: store, Decider: decider, Inbox: inbox}

	run := Run{ID: "r1", Kind: "code", EndedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}

	if err := hook.OnRunEnd(run, []string{"domain1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := store.commitCalls.Load(); got != 1 {
		t.Fatalf("expected 1 commit, got %d", got)
	}

	// nil/empty domains commit no Rec but still commit the run
	store2 := newFakeStore()
	hook2 := EndHook{Store: store2, Decider: decider, Inbox: inbox}
	if err := hook2.OnRunEnd(Run{ID: "r2", Kind: "code", EndedAt: run.EndedAt}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := store2.commitCalls.Load(); got != 1 {
		t.Fatalf("expected 1 commit for nil domains, got %d", got)
	}

	// Chat or Private returns nil before callbacks
	decider.called.Store(false)
	inbox.called.Store(false)
	if err := hook.OnRunEnd(Run{ID: "r3", Kind: "chat", EndedAt: run.EndedAt}, []string{"d"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decider.called.Load() || inbox.called.Load() {
		t.Fatal("chat should not call decider or inbox")
	}

	decider.called.Store(false)
	inbox.called.Store(false)
	if err := hook.OnRunEnd(Run{ID: "r4", Kind: "code", Private: true, EndedAt: run.EndedAt}, []string{"d"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decider.called.Load() || inbox.called.Load() {
		t.Fatal("private should not call decider or inbox")
	}

	// Invalid inputs table
	invalidCases := []struct {
		name    string
		run     Run
		domains []string
		hook    EndHook
	}{
		{"bad kind", Run{ID: "x", Kind: "bad", EndedAt: run.EndedAt}, []string{"d"}, hook},
		{"empty id", Run{ID: "", Kind: "code", EndedAt: run.EndedAt}, []string{"d"}, hook},
		{"whitespace id", Run{ID: "a b", Kind: "code", EndedAt: run.EndedAt}, []string{"d"}, hook},
		{"zero endedat", Run{ID: "x", Kind: "code"}, []string{"d"}, hook},
		{"nil store", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{"d"}, EndHook{Decider: decider, Inbox: inbox}},
		{"nil decider", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{"d"}, EndHook{Store: store, Inbox: inbox}},
		{"nil inbox", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{"d"}, EndHook{Store: store, Decider: decider}},
		{"typed nil store", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{"d"}, EndHook{Store: (*fakeStore)(nil), Decider: decider, Inbox: inbox}},
		{"blank domain", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{""}, hook},
		{"padded domain", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{" d "}, hook},
		{"duplicate domain", Run{ID: "x", Kind: "code", EndedAt: run.EndedAt}, []string{"d", "d"}, hook},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.hook.OnRunEnd(tc.run, tc.domains)
			if !errors.Is(err, ErrInvalidRunEndInput) {
				t.Fatalf("expected ErrInvalidRunEndInput, got %v", err)
			}
		})
	}

	// Duplicate run
	err := hook.OnRunEnd(run, []string{"domain1"})
	if !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("expected ErrDuplicateRun, got %v", err)
	}
	if got := store.commitCalls.Load(); got != 1 {
		t.Fatalf("duplicate should not commit, got %d commits", got)
	}
}
