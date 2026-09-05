package forget

// The interruption tests. Each one stops the pipeline at one of its three
// destructive boundaries and then asserts two things: that the state left
// behind is coherent rather than half-present, and that a later call
// finishes the job. A forget that could be interrupted into a state no
// later call can resolve is the failure these tests exist to catch.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

// TestInterruptedBeforeTheIndexScrub covers a failure at the first
// destructive step. Nothing has been removed, so the record must still be
// fully present: readable AND findable, with the account recording the
// attempt.
func TestInterruptedBeforeTheIndexScrub(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")

	broken := NewPipeline(f.base, f.store, f.clock, f.sink).
		WithIndex(failingScrubber{err: errors.New("the index is unavailable")})
	if _, err := broken.Forget(context.Background(), id, "asked to"); err == nil {
		t.Fatal("a failing index scrub did not fail the forget")
	}

	if _, err := f.store.Read(context.Background(), memory.KindProject, "alpha"); err != nil {
		t.Fatalf("the record became unreadable although nothing was removed: %v", err)
	}
	if len(f.searchHits(t, "pelicans")) != 1 {
		t.Fatal("the record became unfindable although the scrub failed")
	}
	acct, found := f.account(t, memory.KindProject, "alpha")
	if !found || acct.Completed || acct.IndexScrubbed || acct.Tombstoned {
		t.Fatalf("account = %+v (found %v), want an opened but unfinished attempt", acct, found)
	}

	out := f.mustForget(t, id, "asked to")
	if !out.Forgotten || len(f.searchHits(t, "pelicans")) != 0 {
		t.Fatalf("the resumed forget did not finish: %+v", out)
	}
}

// TestInterruptedAfterTheIndexScrub covers the window this pipeline
// deliberately chooses: the index is scrubbed and the record is still on
// disk.
//
// The record must remain READABLE, which is what makes this window
// acceptable. It is not "present but unfindable": the file-scanning verbs
// still return it, only the derived index is briefly behind the files,
// which is the state this package already documents as normal. The account
// records the position, and a later call finishes.
func TestInterruptedAfterTheIndexScrub(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")

	broken := NewPipeline(f.base, failingDeleteStore{inner: f.store, err: errors.New("read-only disk")},
		f.clock, f.sink).WithIndex(f.job)
	if _, err := broken.Forget(context.Background(), id, "asked to"); err == nil {
		t.Fatal("a failing store delete did not fail the forget")
	}

	if _, err := f.store.Read(context.Background(), memory.KindProject, "alpha"); err != nil {
		t.Fatalf("the record is unreadable although it was never retired: %v", err)
	}
	if n := countTombstones(t, f.base); n != 0 {
		t.Fatalf("%d tombstones after a delete that failed, want 0", n)
	}
	acct, found := f.account(t, memory.KindProject, "alpha")
	if !found || acct.Completed || !acct.IndexScrubbed || acct.Tombstoned {
		t.Fatalf("account = %+v (found %v), want the scrub recorded and no tombstone", acct, found)
	}

	// The projection rebuilds the missing row from the files, which is why
	// this window cannot strand a record outside the index.
	f.project(t)
	if len(f.searchHits(t, "pelicans")) != 1 {
		t.Fatal("a projection run did not restore the row of a record that was never retired")
	}

	out := f.mustForget(t, id, "asked to")
	if !out.Forgotten {
		t.Fatalf("the resumed forget did not retire the record: %+v", out)
	}
	if len(f.searchHits(t, "pelicans")) != 0 || countTombstones(t, f.base) != 1 {
		t.Fatal("the resumed forget left the record findable or wrote no tombstone")
	}
}

// TestInterruptedAfterTheTombstone covers the last window: the record is
// gone and the backup lane has not been told. The record must stay gone,
// and the resume must deliver the note without retiring anything twice.
func TestInterruptedAfterTheTombstone(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	f.sink.fail = errors.New("the bus is down")
	f.mustForget(t, id, "asked to")
	f.sink.fail = nil

	if _, err := f.store.Read(context.Background(), memory.KindProject, "alpha"); err == nil {
		t.Fatal("a record that was tombstoned came back")
	}
	if len(f.searchHits(t, "pelicans")) != 0 {
		t.Fatal("a tombstoned record is still findable")
	}

	resumed := f.mustForget(t, id, "asked to")
	if !resumed.EventEmitted || resumed.Forgotten {
		t.Fatalf("resume = %+v, want the note delivered and nothing retired again", resumed)
	}
	if n := countTombstones(t, f.base); n != 1 {
		t.Fatalf("%d tombstones after a resume, want exactly 1", n)
	}
	if n := len(f.sink.events); n != 1 {
		t.Fatalf("%d events after one retirement and one resume, want 1", n)
	}
}

// TestAnInterruptedForgetIsNeverSilent asserts the property that makes
// every window above safe: whatever step a crash lands on, the account on
// disk names the address, the reason and how far the work got.
func TestAnInterruptedForgetIsNeverSilent(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	broken := NewPipeline(f.base, f.store, f.clock, f.sink).
		WithIndex(failingScrubber{err: errors.New("the index is unavailable")})
	if _, err := broken.Forget(context.Background(), id, "a stated reason"); err == nil {
		t.Fatal("the failing scrub did not fail the forget")
	}
	acct, found := f.account(t, memory.KindProject, "alpha")
	switch {
	case !found:
		t.Fatal("an interrupted forget left no account of itself")
	case acct.EntityID != id:
		t.Fatalf("account names %q, want %q", acct.EntityID, id)
	case acct.Reason != "a stated reason":
		t.Fatalf("account reason = %q, want the caller's words", acct.Reason)
	case !acct.RequestedAt.Equal(testEpoch):
		t.Fatalf("account requested_at = %v, want the injected instant", acct.RequestedAt)
	}
}

// TestForgetRefusesAnUnreadableAccount proves the pipeline fails closed on
// an account it cannot parse, rather than starting a fresh one over the
// only surviving explanation of an earlier retirement.
func TestForgetRefusesAnUnreadableAccount(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	path := accountPath(f.base, memory.KindProject, "alpha")
	if err := writeAtomic(path, []byte("{not json")); err != nil {
		t.Fatalf("seeding a damaged account: %v", err)
	}
	_, err := f.pipe.Forget(context.Background(), id, "")
	if !errors.Is(err, ErrMalformedAccount) {
		t.Fatalf("error = %v, want ErrMalformedAccount", err)
	}
	if _, rerr := f.store.Read(context.Background(), memory.KindProject, "alpha"); rerr != nil {
		t.Fatal("the record was retired despite the refusal")
	}
}

// TestForgetRefusesAnAccountFromANewerBuild is the forward-compatibility
// half of the same rule.
func TestForgetRefusesAnAccountFromANewerBuild(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	path := accountPath(f.base, memory.KindProject, "alpha")
	if err := writeAtomic(path, []byte(`{"schema_version":99,"entity_id":"project/alpha"}`)); err != nil {
		t.Fatalf("seeding a future account: %v", err)
	}
	_, err := f.pipe.Forget(context.Background(), id, "")
	if !errors.Is(err, ErrUnsupportedAccountFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedAccountFormat", err)
	}
}
