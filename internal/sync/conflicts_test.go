package sync

// Purpose (this file): the conflict journal itself — deterministic order,
//   concurrent writers, and the nil receiver every merge is allowed to
//   pass.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	stdsync "sync"
	"testing"
)

// TestTheJournalIsOrderedDeterministically is what makes the list surface
// diffable. A merge walks a map, so insertion order is Go's iteration
// order — an order no test can pin and no operator can compare between
// runs.
func TestTheJournalIsOrderedDeterministically(t *testing.T) {
	first := listOf(t)
	for i := range 20 {
		if got := listOf(t); !sameOrder(got, first) {
			t.Fatalf("run %d produced a different order:\n%v\nvs\n%v", i, ids(got), ids(first))
		}
	}
	want := []string{"a", "z", "b", "m"}
	if got := ids(first); !equalStrings(got, want) {
		t.Errorf("order = %v, want %v (by domain, then subkind, then record id)", got, want)
	}
}

// listOf journals four conflicts in a deliberately unsorted order and
// returns the listing.
func listOf(t *testing.T) []Conflict {
	t.Helper()
	j := &ConflictJournal{}
	for _, c := range []Conflict{
		{Domain: "memory", Subkind: "memory", RecordID: "m"},
		{Domain: "config", Subkind: "config", RecordID: "z"},
		{Domain: "config", Subkind: "accounts", RecordID: "a"},
		{Domain: "memory", Subkind: "memory", RecordID: "b"},
	} {
		j.Record(c)
	}
	return j.List()
}

func ids(cs []Conflict) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.RecordID
	}
	return out
}

func sameOrder(a, b []Conflict) bool { return equalStrings(ids(a), ids(b)) }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTheJournalSurvivesConcurrentWriters proves the lock is real. Two
// merges running against one journal is the ordinary case once the engine
// syncs domains in parallel.
func TestTheJournalSurvivesConcurrentWriters(t *testing.T) {
	j := &ConflictJournal{}
	var wg stdsync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range 50 {
				j.Record(Conflict{Domain: "d", Subkind: "s", RecordID: string(rune('a' + w))})
				_ = i
			}
		}(w)
	}
	wg.Wait()
	if got := j.Len(); got != 400 {
		t.Errorf("%d entries, want 400", got)
	}
	if got := len(j.List()); got != 400 {
		t.Errorf("List returned %d entries, want 400", got)
	}
}

// TestANilJournalIsAcceptedNotPanicking is what lets a merge take a
// journal unconditionally. A caller that does not want one passes nil
// rather than every merge growing a branch.
func TestANilJournalIsAcceptedNotPanicking(t *testing.T) {
	var j *ConflictJournal
	j.Record(Conflict{RecordID: "x"})
	if got := j.Len(); got != 0 {
		t.Errorf("a nil journal reports %d entries", got)
	}
	if got := j.List(); got != nil {
		t.Errorf("a nil journal listed %v", got)
	}
}

// TestListReturnsACopy keeps a caller from mutating the journal through
// its own listing, which would make `sync conflicts list` a write surface.
func TestListReturnsACopy(t *testing.T) {
	j := &ConflictJournal{}
	j.Record(Conflict{Domain: "d", Subkind: "s", RecordID: "r", Resolution: ResolutionServerWon})

	got := j.List()
	got[0].Resolution = ResolutionRefused

	if again := j.List(); again[0].Resolution != ResolutionServerWon {
		t.Error("mutating a listing changed the journal")
	}
}
