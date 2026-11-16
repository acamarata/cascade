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

func (s *fakeStore) HasRecommendation(_ string) (bool, error) {
	return false, nil
}

func (s *fakeStore) Commit(run Run, _ string, _ Rec, _ bool) error {
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

func (i *fakeInbox) Notify(_ Rec) error {
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

	testNilDomains(t, decider, inbox, run.EndedAt)
	testSkipsCallbacks(t, hook, decider, inbox, run.EndedAt)

	runInvalidCases(t, hook, store, decider, inbox, run.EndedAt)
	testDuplicateRun(t, hook, store, run)
}

// invalidCase describes a single invalid-input scenario for OnRunEnd.
type invalidCase struct {
	name    string
	run     Run
	domains []string
	hook    EndHook
}

// buildInvalidCases creates the table of invalid-input scenarios.
func buildInvalidCases(hook EndHook, store *fakeStore, decider *fakeDecider, inbox *fakeInbox, endedAt time.Time) []invalidCase {
	return []invalidCase{
		{"bad kind", Run{ID: "x", Kind: "bad", EndedAt: endedAt}, []string{"d"}, hook},
		{"empty id", Run{ID: "", Kind: "code", EndedAt: endedAt}, []string{"d"}, hook},
		{"whitespace id", Run{ID: "a b", Kind: "code", EndedAt: endedAt}, []string{"d"}, hook},
		{"zero endedat", Run{ID: "x", Kind: "code"}, []string{"d"}, hook},
		{"nil store", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{"d"}, EndHook{Decider: decider, Inbox: inbox}},
		{"nil decider", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{"d"}, EndHook{Store: store, Inbox: inbox}},
		{"nil inbox", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{"d"}, EndHook{Store: store, Decider: decider}},
		{"typed nil store", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{"d"}, EndHook{Store: (*fakeStore)(nil), Decider: decider, Inbox: inbox}},
		{"blank domain", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{""}, hook},
		{"padded domain", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{" d "}, hook},
		{"duplicate domain", Run{ID: "x", Kind: "code", EndedAt: endedAt}, []string{"d", "d"}, hook},
	}
}

// runInvalidCases executes each invalid-case subtest against t.
func runInvalidCases(t *testing.T, hook EndHook, store *fakeStore, decider *fakeDecider, inbox *fakeInbox, endedAt time.Time) {
	t.Helper()
	for _, tc := range buildInvalidCases(hook, store, decider, inbox, endedAt) {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.hook.OnRunEnd(tc.run, tc.domains)
			if !errors.Is(err, ErrInvalidRunEndInput) {
				t.Fatalf("expected ErrInvalidRunEndInput, got %v", err)
			}
		})
	}
}

// testDuplicateRun verifies that a duplicate run returns ErrDuplicateRun.
func testDuplicateRun(t *testing.T, hook EndHook, store *fakeStore, run Run) {
	t.Helper()
	err := hook.OnRunEnd(run, []string{"domain1"})
	if !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("expected ErrDuplicateRun, got %v", err)
	}
	if got := store.commitCalls.Load(); got != 1 {
		t.Fatalf("duplicate should not commit, got %d commits", got)
	}
}

// testNilDomains verifies behaviour when no domains are provided.
func testNilDomains(t *testing.T, decider *fakeDecider, inbox *fakeInbox, _ time.Time) {
	t.Helper()
	decider.called.Store(false)
	inbox.called.Store(false)
	store2 := newFakeStore()
	hook2 := EndHook{Store: store2, Decider: decider, Inbox: inbox}
	run2 := Run{ID: "r2", Kind: "code", EndedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := hook2.OnRunEnd(run2, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decider.called.Load() {
		t.Fatal("decider should not be called with nil domains")
	}
	if inbox.called.Load() {
		t.Fatal("inbox should not be called with nil domains")
	}
}

// testSkipsCallbacks verifies that chat and private runs skip callbacks.
func testSkipsCallbacks(t *testing.T, hook EndHook, decider *fakeDecider, inbox *fakeInbox, _ time.Time) {
	t.Helper()
	decider.called.Store(false)
	inbox.called.Store(false)
	chatRun := Run{ID: "r3", Kind: "chat", EndedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := hook.OnRunEnd(chatRun, []string{"d"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decider.called.Load() {
		t.Fatal("decider should not be called for chat runs")
	}
	privRun := Run{ID: "r4", Kind: "code", Private: true, EndedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := hook.OnRunEnd(privRun, []string{"d"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decider.called.Load() {
		t.Fatal("decider should not be called for private runs")
	}
}
