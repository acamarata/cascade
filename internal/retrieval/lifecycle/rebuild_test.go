package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests for Manager.Rebuild — real S-10
// stages (a real sqlite-backed FTS5 Index and a real embed.Pipeline over
// a test-double Embedder, Art.1.1) driven end to end.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
	"github.com/acamarata/cascade/providers/sqlite"
)

// fixedNow is the timestamp every FixedClock in this package's tests
// starts at, shared so migrate_test.go and update_test.go need not
// re-declare it.
var fixedNow = time.Unix(1700000000, 0)

// fakeEmbedder is a test-only Embedder double (Art.1.1: exists ONLY under
// _test.go). It returns one deterministic float per input's byte length,
// so two runs over identical content embed identically.
type fakeEmbedder struct{ calls int }

func (f *fakeEmbedder) Model() provider.EmbedModel {
	return provider.EmbedModel{ID: "lifecycle-test-embed-v1", Dimensions: 1}
}

func (f *fakeEmbedder) Embed(_ context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	f.calls += len(inputs)
	out := make([]provider.EmbedOutput, len(inputs))
	for i, in := range inputs {
		out[i] = provider.EmbedOutput{Vector: []float32{float32(len(in.Text))}, Model: f.Model()}
	}
	return out, nil
}

// fixedSource is a test SourceProvider returning a fixed slice.
type fixedSource struct{ sources []lifecycle.Source }

func (f fixedSource) Sources(context.Context) ([]lifecycle.Source, error) { return f.sources, nil }

// fixedHash is a test GitTreeHashFunc returning a fixed value.
type fixedHash struct{ hash string }

func (f *fixedHash) Get(context.Context) (string, error) { return f.hash, nil }

// harness bundles one lifecycle test's collaborators.
type harness struct {
	t           *testing.T
	dir         string
	store       provider.Store
	index       *retrieval.Index
	vectors     provider.VectorStore
	embedder    *fakeEmbedder
	pipeline    *embed.Pipeline
	clock       *runtime.FixedClock
	catalogPath string
	hash        *fixedHash
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	driver, err := sqlite.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	index, err := retrieval.NewIndex(driver)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	vectors := localvector.New(driver)
	embedder := &fakeEmbedder{}
	clock := runtime.NewFixedClock(fixedNow)
	pipeline, err := embed.New(embedder, vectors, driver, clock, 0)
	if err != nil {
		t.Fatalf("embed.New: %v", err)
	}
	return &harness{
		t: t, dir: dir, store: driver, index: index, vectors: vectors,
		embedder: embedder, pipeline: pipeline, clock: clock,
		catalogPath: filepath.Join(dir, "retrieval", "catalog.json"),
		hash:        &fixedHash{hash: "commit-1:digest-1"},
	}
}

// manager builds a Manager from h's collaborators plus sources.
func (h *harness) manager(sources []lifecycle.Source) *lifecycle.Manager {
	h.t.Helper()
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index, Vectors: h.vectors,
		Pipeline: h.pipeline, Sources: fixedSource{sources: sources}, TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		h.t.Fatalf("NewManager: %v", err)
	}
	return m
}

var testCorpus = corpus.Corpus{
	ID: "handbook", ScopeRef: "project/example",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal, Trust: corpus.TrustTrusted,
}

func oneSource(path, body string) []lifecycle.Source {
	return []lifecycle.Source{{Corpus: testCorpus, Files: []lifecycle.SourceFile{{Path: path, Content: []byte(body)}}}}
}

// TestRecallIndexRebuildWritesChunksAndCatalog proves rebuild writes to
// the FTS5 leg, the vector leg (through the real pipeline), and the
// catalog document, and records the generation marker.
func TestRecallIndexRebuildWritesChunksAndCatalog(t *testing.T) {
	h := newHarness(t)
	m := h.manager(oneSource("a.md", "# Title\n\nhello world"))
	res, err := m.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if res.ChunksWritten == 0 || res.CorporaIndexed != 1 || res.Marker != h.hash.hash {
		t.Fatalf("unexpected result: %+v", res)
	}
	if h.embedder.calls == 0 {
		t.Fatal("want the real pipeline to have embedded at least one chunk")
	}
	data, err := os.ReadFile(h.catalogPath)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var doc recall.CatalogDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(doc.Corpora) != 1 || len(doc.Records) == 0 {
		t.Fatalf("catalog not populated: %+v", doc)
	}
}

// TestRecallIndexRebuildIsConvergent proves a second rebuild over
// unchanged sources reports zero delta (06 §5 rule 9).
func TestRecallIndexRebuildIsConvergent(t *testing.T) {
	h := newHarness(t)
	m := h.manager(oneSource("a.md", "# T\n\nstable content"))
	ctx := context.Background()
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	res, err := m.Rebuild(ctx)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if !res.Converged || res.ChunksWritten != 0 || res.ChunksDeleted != 0 {
		t.Fatalf("want a convergent no-op, got %+v", res)
	}
}

// TestRecallIndexRebuildDeletesRemovedSources proves rebuild retracts
// chunks whose source disappeared.
func TestRecallIndexRebuildDeletesRemovedSources(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\ncontent one"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	m2 := h.manager(nil)
	res, err := m2.Rebuild(ctx)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if res.ChunksDeleted == 0 {
		t.Fatalf("want deleted chunks after removing the only source, got %+v", res)
	}
}

// TestRecallIndexGenerationMarker proves rebuild sets the marker and
// verify reads it back as current.
func TestRecallIndexGenerationMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.MarkerStatus != lifecycle.MarkerCurrent || report.StoredMarker != h.hash.hash {
		t.Fatalf("want a current marker matching %q, got %+v", h.hash.hash, report)
	}
}

// TestRecallIndexRebuildResetsMarker proves a rebuild after the tree hash
// changes stamps the NEW hash, not the old one.
func TestRecallIndexRebuildResetsMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	h.hash.hash = "commit-2:digest-2"
	res, err := m.Rebuild(ctx)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if res.Marker != "commit-2:digest-2" {
		t.Fatalf("want the marker reset to the new hash, got %q", res.Marker)
	}
}

// TestRecallIndexNewManagerRequiresDeps proves NewManager validates every
// required field (the error paths acceptance criteria call for).
func TestRecallIndexNewManagerRequiresDeps(t *testing.T) {
	h := newHarness(t)
	base := lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index, TreeHash: h.hash.Get, Clock: h.clock,
	}
	cases := map[string]func(o lifecycle.ManagerOptions) lifecycle.ManagerOptions{
		"no catalog path": func(o lifecycle.ManagerOptions) lifecycle.ManagerOptions { o.CatalogPath = ""; return o },
		"no store":        func(o lifecycle.ManagerOptions) lifecycle.ManagerOptions { o.Store = nil; return o },
		"no index":        func(o lifecycle.ManagerOptions) lifecycle.ManagerOptions { o.Index = nil; return o },
		"no tree hash":    func(o lifecycle.ManagerOptions) lifecycle.ManagerOptions { o.TreeHash = nil; return o },
		"no clock":        func(o lifecycle.ManagerOptions) lifecycle.ManagerOptions { o.Clock = nil; return o },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := lifecycle.NewManager(mutate(base)); !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("want KindInvalidInput, got %v", err)
			}
		})
	}
}
