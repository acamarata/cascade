package forget

// The pipeline is exercised against a REAL file store, a REAL modernc
// SQLite database and the REAL local vector index over it (Article 2:
// both are on the real-counterpart list). Nothing about the storage layer
// is simulated. The only doubles are the embedder, which stands in for a
// network service, and the event sink, which stands in for a bus this
// package deliberately does not import.

import (
	"context"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
	"github.com/acamarata/cascade/providers/sqlite"
)

// testEpoch is the single instant every fixture's clock starts at, so no
// assertion in this package depends on the wall clock.
var testEpoch = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// fixture is one test's whole world.
type fixture struct {
	base  string
	store *memory.FileStore
	job   *memory.ProjectionJob
	kv    provider.Store
	vec   provider.VectorStore
	clock *testkit.FrozenClock
	sink  *recordingSink
	pipe  *Pipeline
}

// newFixture builds a store, a projection over it and a pipeline wired to
// both, inside the test's own temp directory (Article 7: no HOME, no
// system temp).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	clk := testkit.NewFrozenClock(testEpoch)
	store := memory.NewFileStore(base, clk)
	db := openTestDB(t)
	vec := localvector.New(db)
	job := memory.NewProjectionJob(store, db, newEmbedder(), vec, clk)
	sink := &recordingSink{}
	return &fixture{
		base: base, store: store, job: job, kv: db, vec: vec,
		clock: clk, sink: sink,
		pipe: NewPipeline(base, store, clk, sink).WithIndex(job),
	}
}

// openTestDB opens a real SQLite database in the test's own directory.
func openTestDB(t *testing.T) *sqlite.Driver {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// remember writes one record and projects it, so every test starts from a
// record that is both on disk and in the index.
func (f *fixture) remember(t *testing.T, kind memory.MemoryKind, name, body string) string {
	t.Helper()
	entry := memory.MemoryEntry{
		Name: name, Kind: kind, Body: body, Description: name + " description",
		Provenance: memory.Provenance{Origin: memory.OriginSession, SessionID: "s-1"},
		ScopeRef:   "local", Confidence: 1,
	}
	if err := f.store.Write(context.Background(), entry); err != nil {
		t.Fatalf("writing %s/%s: %v", kind, name, err)
	}
	f.project(t)
	return memory.Address(kind, name)
}

// project brings the index up to date with the files.
func (f *fixture) project(t *testing.T) {
	t.Helper()
	if _, err := f.job.Run(context.Background()); err != nil {
		t.Fatalf("projecting: %v", err)
	}
}

// searchHits reports how many records the index returns for a query.
func (f *fixture) searchHits(t *testing.T, query string) []memory.IndexedRecord {
	t.Helper()
	hits, err := f.job.Search(context.Background(), query, 0)
	if err != nil {
		t.Fatalf("searching for %q: %v", query, err)
	}
	return hits
}

// vectorCount returns how many embeddings the index holds.
func (f *fixture) vectorCount(t *testing.T) int {
	t.Helper()
	n, err := f.vec.Count(context.Background(), string(storage.DomainMemory))
	if err != nil {
		t.Fatalf("counting vectors: %v", err)
	}
	return n
}

// mustForget runs the pipeline and fails the test on any error.
func (f *fixture) mustForget(t *testing.T, id, reason string) memory.ForgetOutcome {
	t.Helper()
	out, err := f.pipe.Forget(context.Background(), id, reason)
	if err != nil {
		t.Fatalf("Forget(%s): %v", id, err)
	}
	return out
}

// account reads the stored account for an address.
func (f *fixture) account(t *testing.T, kind memory.MemoryKind, name string) (account, bool) {
	t.Helper()
	a, found, err := loadAccount(accountPath(f.base, kind, name))
	if err != nil {
		t.Fatalf("loading the account for %s/%s: %v", kind, name, err)
	}
	return a, found
}

// traceFor returns the trace for one place, and fails when the outcome
// does not mention that place at all. Every test that asserts a
// disposition goes through this, so an outcome that silently dropped a
// place fails rather than passing by omission.
func traceFor(t *testing.T, out memory.ForgetOutcome, place string) memory.ForgetTrace {
	t.Helper()
	for _, tr := range out.Traces {
		if tr.Place == place {
			return tr
		}
	}
	t.Fatalf("the outcome named no trace for %q; it listed %v", place, places(out))
	return memory.ForgetTrace{}
}

// places lists every place an outcome reported, for failure messages.
func places(out memory.ForgetOutcome) []string {
	out2 := make([]string, 0, len(out.Traces))
	for _, tr := range out.Traces {
		out2 = append(out2, tr.Place)
	}
	return out2
}

// recordingSink captures every event the pipeline offers it, and can be
// told to fail.
type recordingSink struct {
	events []memory.ForgottenEvent
	fail   error
}

func (s *recordingSink) MemoryForgotten(_ context.Context, ev memory.ForgottenEvent) error {
	if s.fail != nil {
		return s.fail
	}
	s.events = append(s.events, ev)
	return nil
}

// fixedEmbedder derives a vector from the text's own digest, so the same
// body always embeds to the same vector with no network call.
type fixedEmbedder struct{ model provider.EmbedModel }

func newEmbedder() *fixedEmbedder {
	return &fixedEmbedder{model: provider.EmbedModel{ID: "test-embed-v1", Dimensions: 4}}
}

func (e *fixedEmbedder) Model() provider.EmbedModel { return e.model }

func (e *fixedEmbedder) Embed(_ context.Context, in []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	out := make([]provider.EmbedOutput, 0, len(in))
	for _, item := range in {
		digest := memory.HashBody(item.Text)
		vals := make([]float32, e.model.Dimensions)
		for i := range vals {
			vals[i] = float32(binary.BigEndian.Uint32([]byte(digest[i*8:i*8+8])) % 1000)
		}
		out = append(out, provider.EmbedOutput{Vector: vals, Model: e.model})
	}
	return out, nil
}

// wantKind fails unless err carries the expected taxonomy kind.
func wantKind(t *testing.T, err error, kind cascade.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a %s error, got nil", kind)
	}
	if !cascade.HasKind(err, kind) {
		t.Fatalf("error %v does not carry kind %s", err, kind)
	}
}

// walk visits every regular file under root, so a test can assert on the
// whole tree rather than on the paths it already expects.
func walk(root string, visit func(path string)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			visit(path)
		}
		return nil
	})
}

// treeSnapshot returns every file under root with its bytes, so a test can
// prove a forget changed exactly the files it should and no others.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := walk(root, func(path string) {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("reading %s: %v", path, rerr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			t.Fatalf("relativising %s: %v", path, rerr)
		}
		out[rel] = string(data)
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

// failingScrubber refuses every scrub, standing in for an index that is
// unavailable at the moment a forget runs.
type failingScrubber struct{ err error }

func (s failingScrubber) ScrubRecord(context.Context, string) (memory.IndexTrace, error) {
	return memory.IndexTrace{}, s.err
}

// failingDeleteStore is a real store whose Delete refuses, standing in for
// an interruption after the index scrub and before the record is retired.
type failingDeleteStore struct {
	inner RecordStore
	err   error
}

func (s failingDeleteStore) Exists(ctx context.Context, kind memory.MemoryKind, name string) (bool, error) {
	return s.inner.Exists(ctx, kind, name)
}

func (s failingDeleteStore) Delete(context.Context, memory.MemoryKind, string) error { return s.err }
