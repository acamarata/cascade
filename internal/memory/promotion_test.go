package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestLadderRungsMatchTheSpecifiedThresholds asserts the rungs against the
// numbers the plan states, written out here as literals: "≥3 references
// across ≥2 distinct sessions". The expectations are NOT computed from
// ReadyForPromotion or from the exported constants, because a table
// derived from the implementation agrees with the implementation whatever
// the implementation does, and that is exactly how a wrong ladder passes
// review.
func TestLadderRungsMatchTheSpecifiedThresholds(t *testing.T) {
	cases := []struct {
		name     string
		status   CandidateStatus
		refs     int
		sessions []string
		want     bool
	}{
		{"no evidence", CandidatePending, 0, nil, false},
		{"one reference, one session", CandidatePending, 1, []string{"a"}, false},
		{"two references, two sessions", CandidatePending, 2, []string{"a", "b"}, false},
		{"three references, one session", CandidatePending, 3, []string{"a"}, false},
		{"three references, two sessions", CandidatePending, 3, []string{"a", "b"}, true},
		{"three references, three sessions", CandidatePending, 3, []string{"a", "b", "c"}, true},
		{"four references, two sessions", CandidatePending, 4, []string{"a", "b"}, true},
		{"already promoted", CandidatePromoted, 9, []string{"a", "b", "c"}, false},
		{"reverted", CandidateReverted, 9, []string{"a", "b", "c"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadyForPromotion(CandidateEntry{
				Name: "a-record", Kind: KindProject, Status: tc.status,
				RefCount: tc.refs, SessionIDs: tc.sessions,
			})
			if got != tc.want {
				t.Errorf("ReadyForPromotion(%d refs, %d sessions, %s) = %v, want %v",
					tc.refs, len(tc.sessions), tc.status, got, tc.want)
			}
		})
	}
}

// TestPublishedThresholdsAreTheSpecifiedNumbers pins the constants to the
// plan's own numbers, so a later edit to either one is a visible change
// and not a silent loosening of the door.
func TestPublishedThresholdsAreTheSpecifiedNumbers(t *testing.T) {
	if PromotionMinRefCount != 3 {
		t.Errorf("PromotionMinRefCount = %d, want 3", PromotionMinRefCount)
	}
	if PromotionMinSessions != 2 {
		t.Errorf("PromotionMinSessions = %d, want 2", PromotionMinSessions)
	}
}

// TestLadderPromotesOnlyAtTheBoundary drives the real ladder over the real
// ledger and store, one observation at a time, and asserts on which call
// the door opens.
func TestLadderPromotesOnlyAtTheBoundary(t *testing.T) {
	cases := []struct {
		name        string
		sessions    []string
		promoteOn   int // 1-based observation that must promote, 0 for none
		wantRecord  bool
		wantRefsAtP int
	}{
		{"three distinct sessions", []string{"s-1", "s-2", "s-3"}, 3, true, 3},
		{"one session repeated", []string{"s-1", "s-1", "s-1", "s-1"}, 0, false, 0},
		{"two sessions repeated", []string{"s-1", "s-2", "s-1", "s-2"}, 0, false, 0},
		{"two sessions then a third", []string{"s-1", "s-1", "s-2", "s-2", "s-3"}, 5, true, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newLedger(t)
			ladder := NewPromotionLadder(f.ledger)

			for i, s := range tc.sessions {
				entry, promoted, err := ladder.Observe(ctx, observation(s))
				if err != nil {
					t.Fatalf("observation %d: %v", i+1, err)
				}
				if promoted != (i+1 == tc.promoteOn) {
					t.Fatalf("observation %d promoted = %v, want %v", i+1, promoted, i+1 == tc.promoteOn)
				}
				if promoted && entry.RefCount != tc.wantRefsAtP {
					t.Errorf("promoted with RefCount %d, want %d", entry.RefCount, tc.wantRefsAtP)
				}
			}
			_, err := f.store.Read(ctx, KindProject, "a-record")
			if tc.wantRecord && err != nil {
				t.Errorf("no durable record after promotion: %v", err)
			}
			if !tc.wantRecord && !errors.Is(err, ErrNoSuchEntry) {
				t.Errorf("a durable record exists without promotion: %v", err)
			}
		})
	}
}

// TestRefCountNeverExceedsDistinctSessions records the consequence of the
// session-dedup rule the contract states: because a repeat inside one
// session does not count, the reference count and the distinct-session
// count move together. The two thresholds are still both evaluated, and
// this test is what makes the coupling deliberate rather than accidental.
func TestRefCountNeverExceedsDistinctSessions(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	for _, s := range []string{"s-1", "s-1", "s-2", "s-1", "s-2", "s-2"} {
		got, err := f.ledger.Observe(ctx, observation(s))
		if err != nil {
			t.Fatalf("Observe(%s): %v", s, err)
		}
		if got.RefCount != len(got.SessionIDs) {
			t.Fatalf("RefCount %d, distinct sessions %d: the two must move together",
				got.RefCount, len(got.SessionIDs))
		}
	}
}

// TestLadderIsIdempotentAfterPromotion: further observations of a promoted
// candidate promote nothing again and emit nothing again, so a chatty
// caller cannot rewrite a durable record through the ladder.
func TestLadderIsIdempotentAfterPromotion(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	ladder := NewPromotionLadder(f.ledger)
	for _, s := range []string{"s-1", "s-2", "s-3"} {
		if _, _, err := ladder.Observe(ctx, observation(s)); err != nil {
			t.Fatalf("Observe(%s): %v", s, err)
		}
	}
	for _, s := range []string{"s-4", "s-5", "s-6"} {
		entry, promoted, err := ladder.Observe(ctx, observation(s))
		if err != nil {
			t.Fatalf("Observe(%s) after promotion: %v", s, err)
		}
		if promoted {
			t.Fatalf("Observe(%s) promoted an already-promoted candidate", s)
		}
		if entry.RefCount != 3 {
			t.Fatalf("RefCount moved to %d after promotion", entry.RefCount)
		}
	}
	if len(f.sink.promotions) != 1 {
		t.Errorf("promotion events = %d, want exactly 1", len(f.sink.promotions))
	}
}

// TestLadderRepromotesAfterRevertOnFreshEvidence: a reverted candidate has
// to earn the door again from one reference, not slip back through on the
// count it had before.
func TestLadderRepromotesAfterRevertOnFreshEvidence(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	ladder := NewPromotionLadder(f.ledger)
	for _, s := range []string{"s-1", "s-2", "s-3"} {
		if _, _, err := ladder.Observe(ctx, observation(s)); err != nil {
			t.Fatalf("Observe(%s): %v", s, err)
		}
	}
	if _, err := f.ledger.Revert(ctx, KindProject, "a-record", "wrong"); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	for i, s := range []string{"s-7", "s-8", "s-9"} {
		_, promoted, err := ladder.Observe(ctx, observation(s))
		if err != nil {
			t.Fatalf("Observe(%s) after revert: %v", s, err)
		}
		if promoted != (i == 2) {
			t.Fatalf("observation %d after revert promoted = %v, want %v", i+1, promoted, i == 2)
		}
	}
	if len(f.sink.promotions) != 2 {
		t.Errorf("promotion events = %d, want 2", len(f.sink.promotions))
	}
}

// TestLadderRefusesUnusableEvidenceWithoutPromoting is the fail-closed
// case: an observation the ledger will not record cannot open the door,
// whatever else has accumulated.
func TestLadderRefusesUnusableEvidenceWithoutPromoting(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	ladder := NewPromotionLadder(f.ledger)
	if _, _, err := ladder.Observe(ctx, observation("s-1")); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, _, err := ladder.Observe(ctx, observation("s-2")); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	bad := observation("s-3")
	bad.Draft.ScopeRef = ""
	entry, promoted, err := ladder.Observe(ctx, bad)
	if !errors.Is(err, ErrInvalidScopeRef) {
		t.Fatalf("Observe with an unusable draft: %v, want ErrInvalidScopeRef", err)
	}
	if promoted || entry.RefCount != 0 {
		t.Errorf("a refused observation returned promoted=%v entry=%+v", promoted, entry)
	}
	if _, err := f.store.Read(ctx, KindProject, "a-record"); !errors.Is(err, ErrNoSuchEntry) {
		t.Errorf("a durable record was written from refused evidence: %v", err)
	}
}

// ExamplePromotionLadder shows the mechanical lane: three sessions
// observing the same thing promote it into durable memory, with no model
// call and no prompt.
func ExamplePromotionLadder() {
	dir, err := os.MkdirTemp("", "candidate-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ctx := context.Background()
	clock := exampleClock{at: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	store := NewFileStore(dir, clock)
	ladder := NewPromotionLadder(NewFileCandidateLedger(dir, store, clock, nil))

	draft := MemoryEntry{
		Name: "units-and-clock", Kind: KindUser,
		Description: "Stated unit preference",
		Body:        "Prefers metric units.\n",
		ScopeRef:    "global", Confidence: 0.9,
		Provenance: Provenance{Origin: OriginSession, SessionID: "s-3"},
	}
	for _, session := range []string{"s-1", "s-2", "s-3"} {
		entry, promoted, err := ladder.Observe(ctx, Observation{SessionID: session, Draft: draft})
		if err != nil {
			fmt.Println("observe:", err)
			return
		}
		fmt.Printf("%s: refs=%d promoted=%v\n", session, entry.RefCount, promoted)
	}
	// Output:
	// s-1: refs=1 promoted=false
	// s-2: refs=2 promoted=false
	// s-3: refs=3 promoted=true
}
