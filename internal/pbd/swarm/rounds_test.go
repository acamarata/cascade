package swarm

import (
	"errors"
	"testing"
)

// TestSplitPassCalledBetweenRounds verifies that 3 rounds call split 2 times.
func TestSplitPassCalledBetweenRounds(t *testing.T) {
	calls := 0
	split := func() (int, error) {
		calls++
		return 0, nil
	}
	round := func(_ int) error { return nil }
	ladder := &Ladder{}
	if err := RunRounds(3, round, split, ladder); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 split calls, got %d", calls)
	}
}

// TestLadderSplitsAssigned verifies that a split returning 4 sets Ladder.Splits to 4.
func TestLadderSplitsAssigned(t *testing.T) {
	split := func() (int, error) { return 4, nil }
	round := func(_ int) error { return nil }
	ladder := &Ladder{}
	if err := RunRounds(2, round, split, ladder); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ladder.Splits != 4 {
		t.Fatalf("expected Splits=4, got %d", ladder.Splits)
	}
}

// TestSplitPassErrorStopsRun verifies that a split error stops further rounds.
func TestSplitPassErrorStopsRun(t *testing.T) {
	errSplit := errors.New("split failed")
	split := func() (int, error) { return 0, errSplit }
	roundCalls := 0
	round := func(_ int) error {
		roundCalls++
		return nil
	}
	ladder := &Ladder{}
	err := RunRounds(2, round, split, ladder)
	if !errors.Is(err, errSplit) {
		t.Fatalf("expected split error, got %v", err)
	}
	if roundCalls != 1 {
		t.Fatalf("expected 1 round call, got %d", roundCalls)
	}
}

// TestZeroRoundsNeverSplits verifies that 0 rounds returns nil and never calls split.
func TestZeroRoundsNeverSplits(t *testing.T) {
	calls := 0
	split := func() (int, error) {
		calls++
		return 0, nil
	}
	round := func(i int) error { return nil }
	ladder := &Ladder{}
	if err := RunRounds(0, round, split, ladder); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected 0 split calls, got %d", calls)
	}
}
