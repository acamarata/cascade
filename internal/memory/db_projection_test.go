package memory

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
	"github.com/acamarata/cascade/providers/sqlite"
)

// The projection is exercised against a REAL modernc SQLite database file
// and the real local vector index over it (Article 2: SQLite is on the
// real-counterpart list). Nothing about the storage layer is simulated.
// The only double is the embedder, which stands in for a network service
// and is confined to this file.

// fixedEmbedder is the test-only Embedder. It derives a vector from the
// text's own BLAKE3 digest, so the same body always embeds to the same
// vector and two different bodies almost never collide, without any
// network call or model.
type fixedEmbedder struct {
	model provider.EmbedModel
	calls int
	// failOn makes the backend fail for any text containing it.
	failOn string
	// wrongWidth makes the embedder violate its own contract by
	// returning a vector narrower than the model it reports.
	wrongWidth bool
}

func newEmbedder() *fixedEmbedder {
	return &fixedEmbedder{model: provider.EmbedModel{ID: "test-embed-v1", Dimensions: 4}}
}

func (e *fixedEmbedder) Model() provider.EmbedModel { return e.model }

func (e *fixedEmbedder) Embed(_ context.Context, in []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	e.calls++
	out := make([]provider.EmbedOutput, 0, len(in))
	for _, item := range in {
		if e.failOn != "" && strings.Contains(item.Text, e.failOn) {
			return nil, cascade.New(cascade.KindUnavailable, "embedder unavailable")
		}
		digest := HashBody(item.Text)
		vals := make([]float32, e.model.Dimensions)
		for i := range vals {
			chunk := digest[i*8 : i*8+8]
			vals[i] = float32(binary.BigEndian.Uint32([]byte(chunk)) % 1000)
		}
		if e.wrongWidth {
			vals = vals[:len(vals)-1]
		}
		out = append(out, provider.EmbedOutput{Vector: vals, Model: e.model})
	}
	return out, nil
}

// projectionFixture is one test's whole world: a real file store, a real
// SQLite database, a real vector index over it, and a frozen clock.
type projectionFixture struct {
	job      *ProjectionJob
	files    *FileStore
	kv       provider.Store
	vectors  provider.VectorStore
	embedder *fixedEmbedder
	clock    *testClockRef
	base     string
}

func newProjection(t *testing.T) *projectionFixture {
	t.Helper()
	files, clk, base := newStore(t)
	db := openTestDB(t)
	emb := newEmbedder()
	vec := localvector.New(db)
	return &projectionFixture{
		job:   NewProjectionJob(files, db, emb, vec, files.clock),
		files: files, kv: db, vectors: vec, embedder: emb, clock: clk, base: base,
	}
}

// openTestDB opens a real SQLite database inside the test's own temp
// directory (Article 7: no HOME, no system temp) and closes it with the
// test.
func openTestDB(t *testing.T) *sqlite.Driver {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeEntry(t *testing.T, f *projectionFixture, name, body, desc string) MemoryEntry {
	t.Helper()
	e := validEntry()
	e.Name, e.Body, e.Description = name, body, desc
	if err := f.files.Write(context.Background(), e); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return e
}

func mustRun(t *testing.T, f *projectionFixture) ProjectionResult {
	t.Helper()
	res, err := f.job.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRun_ProjectsAndIsIdempotent(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "the alpha body mentions pelicans\n", "first")
	writeEntry(t, f, "beta", "the beta body mentions cormorants\n", "second")

	first := mustRun(t, f)
	if first.Upserted != 2 || first.Scanned != 2 || first.Embedded != 2 {
		t.Fatalf("first run = %+v, want 2 scanned, 2 upserted, 2 embedded", first)
	}
	if !first.Rebuilt {
		t.Fatal("the first run over an unstamped projection did not rebuild")
	}

	callsAfterFirst := f.embedder.calls
	second := mustRun(t, f)
	if second.Upserted != 0 || second.Skipped != 2 || second.Embedded != 0 {
		t.Fatalf("second run = %+v, want 0 upserted, 2 skipped, 0 embedded", second)
	}
	if second.Rebuilt {
		t.Fatal("the second run rebuilt a projection whose version already matched")
	}
	if f.embedder.calls != callsAfterFirst {
		t.Fatalf("the second run embedded again: %d calls, want %d", f.embedder.calls, callsAfterFirst)
	}
}

// TestSearch_TermInBodyReturnsRecord is the full-text acceptance test:
// after projecting known entries, a term present in one body returns that
// record and only that record, from the real database.
func TestRun_BodyChangeReprojectsAndReembeds(t *testing.T) {
	f := newProjection(t)
	e := writeEntry(t, f, "alpha", "original body pelicans\n", "first")
	mustRun(t, f)

	e.Body = "rewritten body cormorants\n"
	if err := f.files.Update(context.Background(), e); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res := mustRun(t, f)
	if res.Upserted != 1 || res.Embedded != 1 {
		t.Fatalf("run after a body change = %+v, want 1 upserted and 1 embedded", res)
	}

	row := mustRow(t, f, "project/alpha")
	if row.ContentHash != HashBody(e.Body) {
		t.Fatalf("row hash %q does not match the file's body hash %q", row.ContentHash, HashBody(e.Body))
	}
	assertHits(t, f, "cormorants", "project/alpha")
	assertHits(t, f, "pelicans")
}

// TestRun_MetadataChangeReprojectsWithoutReembedding pins the dedupe rule:
// a changed description must reach the index, and must not pay for an
// embedding the unchanged body would produce identically.
func TestRun_MetadataChangeReprojectsWithoutReembedding(t *testing.T) {
	f := newProjection(t)
	e := writeEntry(t, f, "alpha", "unchanged body\n", "first")
	mustRun(t, f)
	calls := f.embedder.calls

	e.Description = "rewritten description"
	if err := f.files.Update(context.Background(), e); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res := mustRun(t, f)
	if res.Upserted != 1 || res.Embedded != 0 {
		t.Fatalf("run after a description change = %+v, want 1 upserted and 0 embedded", res)
	}
	if f.embedder.calls != calls {
		t.Fatal("an unchanged body was embedded again")
	}
	if row := mustRow(t, f, "project/alpha"); row.Description != e.Description {
		t.Fatalf("row kept the stale description %q", row.Description)
	}
}

// TestRun_FileRemovedWithoutTombstoneRetiresTheRow is why deletion is
// detected by diffing the listing rather than by scanning for tombstones:
// a record file removed with an ordinary rm leaves no tombstone at all.
// TestRun_UnsupportedVersionIsNeverIndexed is the visibility rule: a record
// the file store refuses must not be reachable through the index either,
// and the refusal must be reported rather than silent.
// TestRun_OneDamagedFileDoesNotFailTheRun preserves the store's own rule
// that one bad file costs exactly one record.
// TestSearch_ExpiredRecordIsNotReturned pins the narrowing rule: a TTL that
// has passed removes the record from search results, judged against the
// injected clock and never the wall clock.
// TestRebuild_RecoversFromADeletedProjection is the source-of-truth test:
// the projection is destroyed, and the files alone put it back exactly.
// TestRun_CorruptRowIsRewrittenFromTheFile pins which side wins: a row that
// disagrees with the file, or cannot be read at all, is replaced from the
// file. The file is never written back from the row.
// TestSearch_CorruptRowRefusesRatherThanShortening pins the fail-closed
// read: a query over a projection it cannot decode reports the integrity
// failure instead of quietly returning fewer results.
// TestRun_VersionMismatchForcesARebuild proves the stamp is load-bearing:
// rows written under another layout version are not patched, they are
// dropped and rebuilt from the files.
// TestRun_WithoutAVectorLegStillProjects covers the profile that has no
// embedding provider configured: rows and postings still land.
// TestRun_EmbedderFailureIsReportedAndTheRowStillLands keeps a failing
// provider from costing the whole run, and from being silent about it.
// TestRun_EmbedderContractViolationIsRefused proves the batch check is not
// decorative: a provider that returns a wrong-width vector is refused
// rather than written into an index that then compares two spaces.
// TestRun_RetiredRecordComesBack covers the row's second life: a name
// written again after a delete flips the same row live.
// TestRun_StoreFailurePropagates covers the fatal path: a projection store
// that cannot be written fails the run rather than reporting success over
// an index it did not update.
// failingStore is a real store whose writes fail, for the fatal path only.
func (failingStore) Put(_ context.Context, _, _ string, _ []byte) error {
	return cascade.New(cascade.KindUnavailable, "disk gone")
}

// TestRun_ScanFailurePropagates covers the read path's fatal branch: a
// projection store whose scan fails must fail the run, never report a
// success over rows it could not see.
// unscannableStore is a real store whose scans fail, for the fatal path.
func (unscannableStore) Scan(_ context.Context, _, _ string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "disk gone")
}

func mustRow(t *testing.T, f *projectionFixture, id string) IndexedRecord {
	t.Helper()
	row, found, err := readRow(context.Background(), f.kv, id)
	if err != nil || !found {
		t.Fatalf("reading row %s: found=%v err=%v", id, found, err)
	}
	return row
}

func mustVectorCount(t *testing.T, f *projectionFixture) int {
	t.Helper()
	n, err := f.vectors.Count(context.Background(), projectionNamespace)
	if err != nil {
		t.Fatalf("counting vectors: %v", err)
	}
	return n
}

func mustVersion(t *testing.T, f *projectionFixture) int {
	t.Helper()
	v, err := f.job.readVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the version: %v", err)
	}
	return v
}

// assertHits asserts the exact set of ids a query returns, in order.
func assertHits(t *testing.T, f *projectionFixture, query string, want ...string) {
	t.Helper()
	hits, err := f.job.Search(context.Background(), query, 0)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	if len(hits) != len(want) {
		t.Fatalf("Search(%q) returned %d hits, want %d", query, len(hits), len(want))
	}
	for i := range want {
		if hits[i].ID != want[i] {
			t.Fatalf("Search(%q)[%d] = %s, want %s", query, i, hits[i].ID, want[i])
		}
	}
}
