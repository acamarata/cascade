package retrieval_test

// Purpose: the shared harness for the full-text index's tests — a REAL
//   modernc-sqlite database under t.TempDir() (never a mock Store, never
//   an in-memory map), the corpus model the scope filter is resolved from,
//   and the two corpora every scope assertion is made across.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — the database lives under t.TempDir(), no clock, no
//   network. The contract asks for an in-memory SQLite database; the
//   tree's established shape for a real-driver test is a file under
//   t.TempDir() (internal/memory/db_projection_test.go,
//   internal/memory/forget/fixture_test.go, internal/runtime/
//   daemonless_test.go all open sqlite.Open on a TempDir path), and the
//   driver's WAL/flock arbitration is only exercised by a real file, so
//   this follows the tree.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// ftsHarness is one index over a real SQLite database plus the corpus
// model its scope decisions come from.
type ftsHarness struct {
	index *retrieval.Index
	store provider.Store
	model *corpus.Store
}

// ftsHandbook is the corpus the querying session may read.
var ftsHandbook = corpus.Corpus{
	ID: "handbook", ScopeRef: "project/example",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// ftsJournal belongs to a scope the querying session is not in and is
// personal-tier besides. Nothing from it may reach a result.
var ftsJournal = corpus.Corpus{
	ID: "journal", ScopeRef: "user/journal",
	Privacy: corpus.PrivacyPersonal, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// ftsSession is the querying session: entitled to the handbook only.
var ftsSession = corpus.Query{
	Membership:  corpus.Membership{Scope: "project/example"},
	Entitlement: corpus.PrivacyProject,
}

// newFTSHarness opens a real SQLite database and builds the index over it.
func newFTSHarness(t *testing.T) *ftsHarness {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	index, err := retrieval.NewIndex(driver)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	return &ftsHarness{index: index, store: driver, model: corpus.NewStore()}
}

// addCorpus registers a corpus with the model so its records can resolve.
func (h *ftsHarness) addCorpus(t *testing.T, c corpus.Corpus) {
	t.Helper()
	if err := h.model.AddCorpus(c); err != nil {
		t.Fatalf("AddCorpus(%s): %v", c.ID, err)
	}
}

// index writes one document's chunk under c, registering it in the corpus
// model at the same time so the index and the model agree about what
// exists.
func (h *ftsHarness) indexDoc(t *testing.T, c corpus.Corpus, path, body string) retrieval.Chunk {
	t.Helper()
	chunk := retrieval.Chunk{
		ID: retrieval.ChunkID([]byte(body)), Path: path,
		Content: []byte(body), Lang: "markdown", EndByte: len(body),
	}
	if err := h.index.Write(context.Background(), c.ID, []retrieval.Chunk{chunk}); err != nil {
		t.Fatalf("Write(%s): %v", path, err)
	}
	if err := h.model.AddRecord(corpus.Record{
		ID: chunk.ID, CorpusID: c.ID, ScopeRef: c.ScopeRef,
		Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust,
	}); err != nil {
		t.Fatalf("AddRecord(%s): %v", path, err)
	}
	return chunk
}

// filter resolves the scope filter for q against the corpus model.
func (h *ftsHarness) filter(t *testing.T, q corpus.Query) *fusion.ScopeFilter {
	t.Helper()
	f, err := fusion.NewScopeFilter(h.model, q)
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	return f
}

// search runs one query through the index under q's scope.
func (h *ftsHarness) search(t *testing.T, q corpus.Query, text string, topK int) []retrieval.Hit {
	t.Helper()
	hits, err := h.index.Search(context.Background(), retrieval.SearchRequest{
		Text: text, Scope: h.filter(t, q).Predicate(),
	}, topK)
	if err != nil {
		t.Fatalf("Search(%q): %v", text, err)
	}
	return hits
}

// countRows returns how many keys in the retrieval namespace start with
// prefix, which is how the idempotency assertions count real stored rows
// rather than trusting the query path to report on itself.
func (h *ftsHarness) countRows(t *testing.T, prefix string) int {
	t.Helper()
	it, err := h.store.Scan(context.Background(), retrieval.IndexNamespace, prefix)
	if err != nil {
		t.Fatalf("Scan(%q): %v", prefix, err)
	}
	defer func() { _ = it.Close() }()
	n := 0
	for it.Next(context.Background()) {
		n++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("Scan(%q): %v", prefix, err)
	}
	return n
}

// paths maps hits to their source paths, the shape most assertions read.
func paths(hits []retrieval.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path
	}
	return out
}

// corpusQueryFor is the session that owns c: in its scope, entitled to
// its privacy tier. It is the control every scope test needs.
func corpusQueryFor(c corpus.Corpus) corpus.Query {
	return corpus.Query{
		Membership:  corpus.Membership{Scope: c.ScopeRef},
		Entitlement: c.Privacy,
	}
}

// emptyScope is the narrowing of a session authorized to read nothing.
func emptyScope() fusion.ScopePredicate { return fusion.ScopePredicate{} }
