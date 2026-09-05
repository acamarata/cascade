package forget

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestForgetRemovesEveryReachableTrace is the ticket's central claim: a
// forget removes the record, its projected row, its postings and its
// vector, and nothing that could still answer a query for it survives.
func TestForgetRemovesEveryReachableTrace(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	if len(f.searchHits(t, "pelicans")) != 1 {
		t.Fatal("the record was not searchable before the forget, so the test proves nothing")
	}
	vectorsBefore := f.vectorCount(t)

	out := f.mustForget(t, id, "asked to")

	if !out.Forgotten || out.AlreadyForgotten {
		t.Fatalf("outcome = %+v, want Forgotten with AlreadyForgotten false", out)
	}
	if _, err := f.store.Read(context.Background(), memory.KindProject, "alpha"); err == nil {
		t.Fatal("the record is still readable from the store after a forget")
	}
	if hits := f.searchHits(t, "pelicans"); len(hits) != 0 {
		t.Fatalf("the index still answers for a forgotten record: %+v", hits)
	}
	if got := f.vectorCount(t); got != vectorsBefore-1 {
		t.Fatalf("vector count = %d, want %d: the embedding outlived the record", got, vectorsBefore-1)
	}
	if !out.Index.Row || out.Index.Postings == 0 || !out.Index.Vector {
		t.Fatalf("index trace = %+v, want a row, postings and a vector removed", out.Index)
	}
}

// TestForgetScrubIsVerifiedByASecondScrub asserts the absence directly
// rather than trusting the first call's return code: scrubbing the same
// address again must remove nothing.
func TestForgetScrubIsVerifiedByASecondScrub(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	f.mustForget(t, id, "")

	again, err := f.job.ScrubRecord(context.Background(), id)
	if err != nil {
		t.Fatalf("re-scrubbing: %v", err)
	}
	if !again.Empty() {
		t.Fatalf("a second scrub still removed %+v, so the first one left traces", again)
	}
}

// TestForgetEnumeratesEveryPlace is the verification the ticket's own name
// asks for: the outcome must name every place a record leaves a mark, and
// must not claim to have removed the ones it cannot.
func TestForgetEnumeratesEveryPlace(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body mentions pelicans\n")
	out := f.mustForget(t, id, "asked to")

	want := map[string]memory.ForgetDisposition{
		"record file":                       memory.ForgetRemoved,
		"projection rows and postings":      memory.ForgetRemoved,
		"vector index":                      memory.ForgetRemoved,
		"tombstone":                         memory.ForgetRetained,
		"forget account":                    memory.ForgetRetained,
		"backup and sync note":              memory.ForgetRetained,
		"consolidation account":             memory.ForgetRetained,
		"staleness queue":                   memory.ForgetRetained,
		"candidate ledger and review queue": memory.ForgetUnreachable,
		"record bytes on disk":              memory.ForgetUnreachable,
	}
	if len(out.Traces) != len(want) {
		t.Fatalf("the outcome listed %d places, want %d: %v", len(out.Traces), len(want), places(out))
	}
	for place, disposition := range want {
		if got := traceFor(t, out, place).Disposition; got != disposition {
			t.Errorf("%s: disposition %q, want %q", place, got, disposition)
		}
	}
	for _, tr := range out.Traces {
		if strings.TrimSpace(tr.Detail) == "" {
			t.Errorf("%s: reported a disposition with no explanation", tr.Place)
		}
	}
}

// TestForgetDoesNotClaimToShredBytes pins the one claim this pipeline must
// never make. The file is unlinked, not overwritten, and the outcome has
// to say so even on a completely successful forget.
func TestForgetDoesNotClaimToShredBytes(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	out := f.mustForget(t, id, "")

	tr := traceFor(t, out, "record bytes on disk")
	if tr.Disposition != memory.ForgetUnreachable {
		t.Fatalf("bytes disposition = %q, want %q: the pipeline claimed a shred it did not do",
			tr.Disposition, memory.ForgetUnreachable)
	}
	if !strings.Contains(tr.Detail, "not shredded") {
		t.Fatalf("the bytes trace does not say the bytes were not shredded: %q", tr.Detail)
	}
}

// TestForgetIsIdempotent covers the contract's first acceptance criterion:
// a second call returns nil, retires nothing further, and leaves exactly
// one tombstone.
func TestForgetIsIdempotent(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	first := f.mustForget(t, id, "asked to")
	second := f.mustForget(t, id, "asked again")

	if !first.Forgotten || first.AlreadyForgotten {
		t.Fatalf("first outcome = %+v, want a real retirement", first)
	}
	if second.Forgotten || !second.AlreadyForgotten {
		t.Fatalf("second outcome = %+v, want AlreadyForgotten with nothing retired", second)
	}
	if n := len(f.sink.events); n != 1 {
		t.Fatalf("%d events emitted for two calls, want exactly 1", n)
	}
	if n := countTombstones(t, f.base); n != 1 {
		t.Fatalf("%d tombstones on disk, want exactly 1", n)
	}
	acct, found := f.account(t, memory.KindProject, "alpha")
	if !found || acct.Reason != "asked to" {
		t.Fatalf("account = %+v (found %v), want the FIRST call's reason preserved", acct, found)
	}
}

// TestForgetRefusesAnAddressWithNothingToRetire proves the verb fails
// closed on an address that was never stored, rather than reporting a
// success that removed nothing.
func TestForgetRefusesAnAddressWithNothingToRetire(t *testing.T) {
	f := newFixture(t)
	_, err := f.pipe.Forget(context.Background(), "project/never-stored", "")
	wantKind(t, err, cascade.KindNotFound)
	if !errors.Is(err, memory.ErrNoSuchEntry) {
		t.Fatalf("error %v does not wrap ErrNoSuchEntry", err)
	}
	if _, found := f.account(t, memory.KindProject, "never-stored"); found {
		t.Fatal("a refused forget wrote an account for a record that never existed")
	}
}

// TestForgetRefusesAMalformedAddress proves the address is parsed fail
// closed, so no near-miss address is repaired into a record the caller did
// not name.
func TestForgetRefusesAMalformedAddress(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"", "alpha", "nosuchkind/alpha", "project/../escape", "project/.hidden"} {
		_, err := f.pipe.Forget(context.Background(), id, "")
		wantKind(t, err, cascade.KindInvalidInput)
	}
}

// TestForgetRecordsTheContractFields checks the four fields the tombstone
// semantics are specified against: entity id, deleted-at, reason and
// schema version, all from the injected clock.
func TestForgetRecordsTheContractFields(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindFeedback, "beta", "the beta body\n")
	f.mustForget(t, id, "no longer true")

	acct, found := f.account(t, memory.KindFeedback, "beta")
	if !found {
		t.Fatal("no account was written for a completed forget")
	}
	switch {
	case acct.EntityID != id:
		t.Errorf("entity id = %q, want %q", acct.EntityID, id)
	case acct.Reason != "no longer true":
		t.Errorf("reason = %q, want the caller's own words", acct.Reason)
	case acct.SchemaVersion != accountSchemaVersion:
		t.Errorf("schema version = %d, want %d", acct.SchemaVersion, accountSchemaVersion)
	case acct.DeletedAt == nil || !acct.DeletedAt.Equal(testEpoch):
		t.Errorf("deleted_at = %v, want the injected clock's instant %v", acct.DeletedAt, testEpoch)
	case !acct.Completed || !acct.Tombstoned || !acct.IndexScrubbed || !acct.EventEmitted:
		t.Errorf("account = %+v, want every step recorded complete", acct)
	}
	if strings.Contains(readAccountBytes(t, f.base, "feedback", "beta"), "the beta body") {
		t.Fatal("the forget account stored the record's body, which defeats the request")
	}
}

// TestForgetEmitsTheBackupNote asserts the event payload the sync lane
// reads, field by field.
func TestForgetEmitsTheBackupNote(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	out := f.mustForget(t, id, "asked to")

	if len(f.sink.events) != 1 {
		t.Fatalf("%d events, want exactly 1", len(f.sink.events))
	}
	ev := f.sink.events[0]
	if ev.EntityID != id || ev.Reason != "asked to" || !ev.Timestamp.Equal(testEpoch) {
		t.Fatalf("event = %+v, want the address, reason and injected instant", ev)
	}
	if ev.EventName() != memory.MemoryForgottenEvent {
		t.Fatalf("event name = %q, want %q", ev.EventName(), memory.MemoryForgottenEvent)
	}
	if !out.EventEmitted || out.EventError != "" {
		t.Fatalf("outcome = %+v, want the emit reported as done", out)
	}
}

// TestForgetSurvivesAnEventSinkFailure covers the contract's "emit failure
// is logged not fatal": the record is still gone, the call still succeeds,
// and the outcome says the backup lane was not told rather than implying
// it was.
func TestForgetSurvivesAnEventSinkFailure(t *testing.T) {
	f := newFixture(t)
	id := f.remember(t, memory.KindProject, "alpha", "the alpha body\n")
	f.sink.fail = errors.New("the bus is down")

	out := f.mustForget(t, id, "asked to")

	if !out.Forgotten {
		t.Fatal("a sink failure aborted a retirement that had already happened")
	}
	if out.EventEmitted || out.EventError == "" {
		t.Fatalf("outcome = %+v, want the failed emit reported", out)
	}
	if got := traceFor(t, out, "backup and sync note").Disposition; got != memory.ForgetUnreachable {
		t.Fatalf("backup note disposition = %q, want %q", got, memory.ForgetUnreachable)
	}
	if _, err := f.store.Read(context.Background(), memory.KindProject, "alpha"); err == nil {
		t.Fatal("the record survived a forget whose only failure was the event")
	}

	// The account is deliberately left incomplete, so a later call
	// retries the note rather than deciding the lane was told.
	acct, _ := f.account(t, memory.KindProject, "alpha")
	if acct.Completed || acct.EventEmitted {
		t.Fatalf("account = %+v, want it left open for the emit to be retried", acct)
	}
	f.sink.fail = nil
	resumed := f.mustForget(t, id, "asked to")
	if !resumed.EventEmitted || len(f.sink.events) != 1 {
		t.Fatalf("the retry did not deliver the note: %+v, %d events", resumed, len(f.sink.events))
	}
}

// countTombstones counts every tombstone marker under base.
func countTombstones(t *testing.T, base string) int {
	t.Helper()
	n := 0
	err := walk(base, func(path string) {
		if strings.HasSuffix(path, ".md.tombstone") {
			n++
		}
	})
	if err != nil {
		t.Fatalf("walking %s: %v", base, err)
	}
	return n
}

// readAccountBytes returns an account file's raw text.
func readAccountBytes(t *testing.T, base, kind, name string) string {
	t.Helper()
	data, err := os.ReadFile(accountPath(base, memory.MemoryKind(kind), name))
	if err != nil {
		t.Fatalf("reading the account file: %v", err)
	}
	return string(data)
}
