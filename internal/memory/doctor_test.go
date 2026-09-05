package memory

import (
	"context"
	"testing"
)

// TestScrubRecordRemovesRowPostingsAndVector proves the scrub reaches all
// three legs, against a real SQLite database and a real vector index.
func TestScrubRecordRemovesRowPostingsAndVector(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	writeEntry(t, f, "beta", "the beta body mentions cormorants\n", "second")
	mustRun(t, f)
	id := recordID(KindProject, "alpha")

	trace, err := f.job.ScrubRecord(context.Background(), id)
	if err != nil {
		t.Fatalf("ScrubRecord: %v", err)
	}
	if !trace.Row || trace.Postings == 0 || !trace.Vector || !trace.VectorProbed {
		t.Fatalf("trace = %+v, want a row, postings and a vector removed", trace)
	}
	hits, err := f.job.Search(context.Background(), "pelicans", 0)
	if err != nil {
		t.Fatalf("searching: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("the scrubbed record is still findable: %+v", hits)
	}
	other, err := f.job.Search(context.Background(), "cormorants", 0)
	if err != nil {
		t.Fatalf("searching: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("scrubbing one record cost a neighbour its row: %+v", other)
	}
}

// TestScrubRecordIsIdempotent is the verification step: a second scrub
// removes nothing, which is how a caller proves the first one finished
// rather than trusting its return code.
func TestScrubRecordIsIdempotent(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	mustRun(t, f)
	id := recordID(KindProject, "alpha")

	if _, err := f.job.ScrubRecord(context.Background(), id); err != nil {
		t.Fatalf("first scrub: %v", err)
	}
	second, err := f.job.ScrubRecord(context.Background(), id)
	if err != nil {
		t.Fatalf("second scrub: %v", err)
	}
	if !second.Empty() {
		t.Fatalf("second scrub removed %+v, so the first left traces", second)
	}
}

// TestScrubRecordOnAnAbsentAddressRemovesNothing proves a scrub of an
// address the projection never held is a clean no-op rather than an error,
// which is what makes it safe to re-run after an interruption.
func TestScrubRecordOnAnAbsentAddressRemovesNothing(t *testing.T) {
	f := newProjection(t)
	mustRun(t, f)
	trace, err := f.job.ScrubRecord(context.Background(), recordID(KindProject, "never-there"))
	if err != nil {
		t.Fatalf("ScrubRecord on an absent address: %v", err)
	}
	if !trace.Empty() {
		t.Fatalf("trace = %+v, want nothing removed", trace)
	}
}

// TestScrubRecordRemovesAnUndecodableRow proves a row this build cannot
// parse is still removed, and is reported as unreadable rather than as a
// row with no postings.
func TestScrubRecordRemovesAnUndecodableRow(t *testing.T) {
	f := newProjection(t)
	ctx := context.Background()
	id := recordID(KindProject, "alpha")
	if err := f.kv.Put(ctx, projectionNamespace, recordKey(id), []byte("{not json")); err != nil {
		t.Fatalf("seeding a damaged row: %v", err)
	}
	trace, err := f.job.ScrubRecord(ctx, id)
	if err != nil {
		t.Fatalf("ScrubRecord: %v", err)
	}
	if !trace.Row || !trace.RowUnreadable {
		t.Fatalf("trace = %+v, want an unreadable row reported", trace)
	}
	if _, found, rerr := readRow(ctx, f.kv, id); rerr == nil && found {
		t.Fatal("the damaged row survived the scrub")
	}
}

// TestOrphansFindsARowWhoseRecordIsGone is the doctor check: a record
// removed from the files but still answering from the index.
func TestOrphansFindsARowWhoseRecordIsGone(t *testing.T) {
	f := newProjection(t)
	ctx := context.Background()
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	writeEntry(t, f, "beta", "the beta body mentions cormorants\n", "second")
	mustRun(t, f)

	if err := f.files.Delete(ctx, KindProject, "alpha"); err != nil {
		t.Fatalf("deleting the record behind the index's back: %v", err)
	}
	orphans, err := f.job.Orphans(ctx)
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0].ID != recordID(KindProject, "alpha") {
		t.Fatalf("orphans = %+v, want exactly the deleted record", orphans)
	}
}

// TestOrphansIgnoresRetiredAndLiveRows proves the check does not cry wolf.
// A retired row answers no query and is not an orphan; a live record is
// not an orphan either.
func TestOrphansIgnoresRetiredAndLiveRows(t *testing.T) {
	f := newProjection(t)
	ctx := context.Background()
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	writeEntry(t, f, "beta", "the beta body mentions cormorants\n", "second")
	mustRun(t, f)

	if err := f.files.Delete(ctx, KindProject, "alpha"); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	// A projection run retires the row properly, which is the state the
	// check must NOT report.
	mustRun(t, f)

	orphans, err := f.job.Orphans(ctx)
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("orphans = %+v, want none after a projection run retired the row", orphans)
	}
}

// TestOrphansReportsAnUnparseableRowKey proves the check fails closed on a
// key nothing this package could have written: it cannot be matched to a
// file, so it cannot be judged live and is reported rather than skipped.
func TestOrphansReportsAnUnparseableRowKey(t *testing.T) {
	f := newProjection(t)
	ctx := context.Background()
	if err := f.kv.Put(ctx, projectionNamespace, recordKey("not-an-address"), []byte("{}")); err != nil {
		t.Fatalf("seeding a stray key: %v", err)
	}
	orphans, err := f.job.Orphans(ctx)
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0].ID != "not-an-address" {
		t.Fatalf("orphans = %+v, want the stray key reported", orphans)
	}
}

// TestScrubRecordWithoutAVectorLegSaysSo proves the difference between "no
// vectors" and "no vector index" is reported rather than collapsed.
func TestScrubRecordWithoutAVectorLegSaysSo(t *testing.T) {
	f := newProjection(t)
	ctx := context.Background()
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	mustRun(t, f)

	noVectors := NewProjectionJob(f.files, f.kv, nil, nil, f.files.clock)
	trace, err := noVectors.ScrubRecord(ctx, recordID(KindProject, "alpha"))
	if err != nil {
		t.Fatalf("ScrubRecord: %v", err)
	}
	if trace.VectorProbed || trace.Vector {
		t.Fatalf("trace = %+v, want the vector leg reported as not configured", trace)
	}
	if !trace.Row {
		t.Fatal("the row was not removed by a job with no vector leg")
	}
}
