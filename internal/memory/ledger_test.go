package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// recordingSink captures the events a ledger emits, so a test can assert
// that a transition was reported rather than assuming it was.
type recordingSink struct {
	promotions []PromotionEvent
	reverts    []RevertEvent
	failWith   error
}

func (s *recordingSink) CandidatePromoted(_ context.Context, ev PromotionEvent) error {
	s.promotions = append(s.promotions, ev)
	return s.failWith
}

func (s *recordingSink) CandidateReverted(_ context.Context, ev RevertEvent) error {
	s.reverts = append(s.reverts, ev)
	return s.failWith
}

// ledgerFixture is one ledger over a real FileStore, both rooted in a
// fresh temp directory with a frozen clock. The store is the real
// counterpart, not a double: a promotion in these tests writes a real
// record file and is read back through the real store.
type ledgerFixture struct {
	ledger *FileCandidateLedger
	store  *FileStore
	sink   *recordingSink
	base   string
	clock  *testClockRef
}

func newLedger(t *testing.T) ledgerFixture {
	t.Helper()
	base := t.TempDir()
	clk := newTestClock()
	store := NewFileStore(base, clk)
	sink := &recordingSink{}
	return ledgerFixture{
		ledger: NewFileCandidateLedger(base, store, clk, sink),
		store:  store,
		sink:   sink,
		base:   base,
		clock:  &testClockRef{clk},
	}
}

// observation returns an observation from session for the standard draft.
func observation(session string) Observation {
	return Observation{SessionID: session, Draft: validEntry()}
}

// observeAll records one observation per session id, failing the test on
// the first refusal.
func observeAll(t *testing.T, l CandidateLedger, sessions ...string) CandidateEntry {
	t.Helper()
	var got CandidateEntry
	for _, s := range sessions {
		var err error
		got, err = l.Observe(context.Background(), observation(s))
		if err != nil {
			t.Fatalf("Observe(%s): %v", s, err)
		}
	}
	return got
}
func TestObserveStartsCandidateAtOneReference(t *testing.T) {
	f := newLedger(t)
	got := observeAll(t, f.ledger, "s-1")

	if got.Status != CandidatePending {
		t.Errorf("status = %q, want %q", got.Status, CandidatePending)
	}
	if got.RefCount != 1 {
		t.Errorf("RefCount = %d, want 1", got.RefCount)
	}
	if len(got.SessionIDs) != 1 || got.SessionIDs[0] != "s-1" {
		t.Errorf("SessionIDs = %v, want [s-1]", got.SessionIDs)
	}
	if got.PromotedAt != nil || got.SnoozeUntil != nil {
		t.Errorf("new candidate has PromotedAt=%v SnoozeUntil=%v, want both nil",
			got.PromotedAt, got.SnoozeUntil)
	}
}

// TestRepeatObservationFromOneSessionCountsOnce is the anti-gaming
// property: a caller that records the same observation many times inside
// one session must not walk it up the ladder.
func TestRepeatObservationFromOneSessionCountsOnce(t *testing.T) {
	f := newLedger(t)
	got := observeAll(t, f.ledger, "s-1", "s-1", "s-1", "s-1")

	if got.RefCount != 1 {
		t.Errorf("RefCount after four same-session observations = %d, want 1", got.RefCount)
	}
	if len(got.SessionIDs) != 1 {
		t.Errorf("SessionIDs = %v, want exactly one entry", got.SessionIDs)
	}
	if ReadyForPromotion(got) {
		t.Error("a candidate observed only inside one session is ready for promotion")
	}
}

// TestSessionIDsAreASortedSet pins the invariant the counting rule rests
// on: every counted session appears once, in lexical order, whatever order
// the observations arrived in.
func TestSessionIDsAreASortedSet(t *testing.T) {
	f := newLedger(t)
	got := observeAll(t, f.ledger, "s-3", "s-1", "s-2", "s-1", "s-3")

	want := []string{"s-1", "s-2", "s-3"}
	if len(got.SessionIDs) != len(want) {
		t.Fatalf("SessionIDs = %v, want %v", got.SessionIDs, want)
	}
	for i := range want {
		if got.SessionIDs[i] != want[i] {
			t.Fatalf("SessionIDs = %v, want %v", got.SessionIDs, want)
		}
	}
	if got.RefCount != len(want) {
		t.Errorf("RefCount = %d, want %d", got.RefCount, len(want))
	}
}

// TestLedgerStateSurvivesCloseReopen proves the file is the state: a
// second ledger opened over the same directory, sharing nothing in memory
// with the first, reads back the same counts.
func TestLedgerStateSurvivesCloseReopen(t *testing.T) {
	f := newLedger(t)
	observeAll(t, f.ledger, "s-1", "s-2")

	reopened := NewFileCandidateLedger(f.base, f.store, newTestClock(), nil)
	got, err := reopened.Get(context.Background(), KindProject, "a-record")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.RefCount != 2 || len(got.SessionIDs) != 2 {
		t.Errorf("after reopen RefCount=%d SessionIDs=%v, want 2 and two ids",
			got.RefCount, got.SessionIDs)
	}
	if got.Status != CandidatePending {
		t.Errorf("after reopen status = %q, want %q", got.Status, CandidatePending)
	}
}

// TestSameEvidenceProducesIdenticalBytes pins determinism at the file: two
// ledgers given the same observations in different orders must write the
// same bytes, or a promotion decision could depend on arrival order.
func TestSameEvidenceProducesIdenticalBytes(t *testing.T) {
	a := newLedger(t)
	b := newLedger(t)
	observeAll(t, a.ledger, "s-2", "s-1", "s-3")
	observeAll(t, b.ledger, "s-3", "s-2", "s-1")

	path := filepath.Join("candidates", "project", "a-record.candidate.json")
	first, err := os.ReadFile(filepath.Join(a.base, path))
	if err != nil {
		t.Fatalf("reading first candidate file: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(b.base, path))
	if err != nil {
		t.Fatalf("reading second candidate file: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("same evidence produced different bytes:\n%s\n---\n%s", first, second)
	}
}
