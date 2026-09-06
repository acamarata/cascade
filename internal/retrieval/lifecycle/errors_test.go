package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests for the error paths Art.3
// requires — a failing SourceProvider, an invalid corpus, an unsupported
// file extension, a nil pipeline, and a failing store/vector-store —
// exercised to raise internal/retrieval/lifecycle's branch coverage
// toward the Art.4 core-engine floor.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// erroringSource always fails.
type erroringSource struct{}

func (erroringSource) Sources(context.Context) ([]lifecycle.Source, error) {
	return nil, cascade.New(cascade.KindUnavailable, "boom")
}

// TestRecallIndexRebuildSourceProviderError proves a failing
// SourceProvider surfaces its error rather than being swallowed.
func TestRecallIndexRebuildSourceProviderError(t *testing.T) {
	h := newHarness(t)
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index,
		Sources: erroringSource{}, TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Rebuild(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the source provider's error surfaced, got %v", err)
	}
}

// TestRecallIndexRebuildInvalidCorpusRefused proves an unclassified
// corpus is refused before any write happens.
func TestRecallIndexRebuildInvalidCorpusRefused(t *testing.T) {
	h := newHarness(t)
	bad := corpus.Corpus{ID: "bad"} // missing scope/privacy/visibility/trust
	m := h.manager([]lifecycle.Source{{Corpus: bad, Files: []lifecycle.SourceFile{{Path: "a.md", Content: []byte("# x\n\ny")}}}})
	if _, err := m.Rebuild(context.Background()); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("want KindInvalidInput for an unclassified corpus, got %v", err)
	}
}

// TestRecallIndexRebuildSkipsUnsupportedExtension proves a file whose
// extension no chunker recognizes is skipped rather than failing the
// whole rebuild.
func TestRecallIndexRebuildSkipsUnsupportedExtension(t *testing.T) {
	h := newHarness(t)
	m := h.manager([]lifecycle.Source{{Corpus: testCorpus, Files: []lifecycle.SourceFile{
		{Path: "image.png", Content: []byte{0x89, 0x50, 0x4e, 0x47}},
		{Path: "a.md", Content: []byte("# T\n\nreal content")},
	}}})
	res, err := m.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if res.ChunksWritten == 0 {
		t.Fatalf("want the recognized file's chunks written despite the skipped one, got %+v", res)
	}
}

// TestRecallIndexRebuildWithNoPipelineSkipsEmbedding proves a Manager
// with no configured Pipeline still indexes the FTS5 leg, degrading the
// vector leg exactly as registerRecallHandler's own precedent documents.
func TestRecallIndexRebuildWithNoPipelineSkipsEmbedding(t *testing.T) {
	h := newHarness(t)
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index,
		Sources:  fixedSource{sources: oneSource("a.md", "# T\n\nno embedder configured")},
		TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if res.ChunksWritten == 0 {
		t.Fatalf("want the FTS5 leg still written with no pipeline configured, got %+v", res)
	}
	report, err := m.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.VectorIncomplete) != 0 {
		t.Fatalf("want no vector-incompleteness reported with no vector store configured, got %+v", report)
	}
}

// failingVectorStore fails every call, for exercising retract's and
// verify's vector-leg error paths.
type failingVectorStore struct{ provider.VectorStore }

func (failingVectorStore) Delete(context.Context, string, []string) error {
	return cascade.New(cascade.KindUnavailable, "vector delete failed")
}

func (failingVectorStore) Count(context.Context, string) (int, error) {
	return 0, cascade.New(cascade.KindUnavailable, "vector count failed")
}

// TestRecallIndexVerifyVectorCountErrorIsIncomplete proves a failing
// VectorStore.Count is reported as incomplete rather than panicking or
// being silently treated as complete.
func TestRecallIndexVerifyVectorCountErrorIsIncomplete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index, Vectors: failingVectorStore{},
		Sources: fixedSource{sources: oneSource("a.md", "# T\n\nbody")}, TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	report, err := m.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.VectorIncomplete) == 0 {
		t.Fatalf("want the failing vector count reported incomplete, got %+v", report)
	}
}

// TestRecallIndexRebuildVectorDeleteErrorSurfaces proves a failing
// vector-leg delete during retract surfaces as the Rebuild call's error.
func TestRecallIndexRebuildVectorDeleteErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\noriginal"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	m2, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: h.store, Index: h.index, Vectors: failingVectorStore{},
		Sources: fixedSource{}, TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m2.Rebuild(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the vector store's delete error surfaced, got %v", err)
	}
}

// TestRecallIndexMigrateBadDBIsError proves Migrate returns a taxonomy
// error rather than panicking when passed a closed/invalid connection.
func TestRecallIndexMigrateBadDBIsError(t *testing.T) {
	db, _ := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	_, err := lifecycle.Migrate(context.Background(), migrateDeps(db, "", runtime.NewFixedClock(fixedNow)))
	if err == nil {
		t.Fatal("want an error against a closed database")
	}
}
