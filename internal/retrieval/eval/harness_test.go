package eval_test

// Purpose: TestHarness — RunKnownItem/RunSemanticParaphrase driven over a
//   real temporary SQLite FTS5 index and a real localvector store,
//   proving the harness produces usable rankings from the real S-10.T2/
//   S-11.T1 collaborators, never a self-authored substitute.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — the FTS5 database lives only below t.TempDir();
//   no network call (the vector leg's embedder replays the committed
//   recorded fixture).
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/eval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
	"github.com/acamarata/cascade/providers/sqlite"
)

// evalCorpusDef is the one corpus every evaluation document is indexed
// under, and the session every query in this package runs as.
var evalCorpusDef = corpus.Corpus{
	ID: "eval-corpus", ScopeRef: "eval/corpus",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

var evalSession = corpus.Query{
	Membership:  corpus.Membership{Scope: "eval/corpus"},
	Entitlement: corpus.PrivacyProject,
}

// buildEvalHarness ingests fixtureCorpus into a real temporary SQLite
// FTS5 index and a real localvector store (backed by an in-memory KV
// store — the vector data itself is not what this ticket tests; the FTS5
// index and the RRF fusion are), and returns the Legs RunKnownItem and
// RunSemanticParaphrase query through.
func buildEvalHarness(t *testing.T, fixtureCorpus eval.EvalCorpus, embeddings eval.RecordedEmbeddings) eval.Legs {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "eval.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	index, err := retrieval.NewIndex(driver)
	if err != nil {
		t.Fatalf("retrieval.NewIndex: %v", err)
	}

	model := corpus.NewStore()
	if err := model.AddCorpus(evalCorpusDef); err != nil {
		t.Fatalf("AddCorpus: %v", err)
	}

	vectors := localvector.New(storetest.NewMemStore())
	ns := fusion.NamespaceFor(evalCorpusDef.ID)

	for _, doc := range fixtureCorpus.Documents {
		chunk := retrieval.Chunk{ID: doc.ID, Path: doc.ID, Content: []byte(doc.Text), Lang: "markdown", EndByte: len(doc.Text)}
		if err := index.Write(context.Background(), evalCorpusDef.ID, []retrieval.Chunk{chunk}); err != nil {
			t.Fatalf("index.Write(%s): %v", doc.ID, err)
		}
		if err := model.AddRecord(corpus.Record{
			ID: doc.ID, CorpusID: evalCorpusDef.ID, ScopeRef: evalCorpusDef.ScopeRef,
			Privacy: evalCorpusDef.Privacy, Visibility: evalCorpusDef.Visibility, Trust: evalCorpusDef.Trust,
		}); err != nil {
			t.Fatalf("AddRecord(%s): %v", doc.ID, err)
		}
		vec, ok := embeddings.Lookup(doc.Text)
		if !ok {
			t.Fatalf("no recorded vector for document %q", doc.ID)
		}
		if err := vectors.Upsert(context.Background(), ns, []provider.Vector{{ID: doc.ID, Values: vec}}); err != nil {
			t.Fatalf("Upsert(%s): %v", doc.ID, err)
		}
	}

	filter, err := fusion.NewScopeFilter(model, evalSession)
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	vectorLeg := fusion.NewVectorLeg(eval.NewRecordedVectorEmbedder(embeddings), vectors, nil)
	return eval.Legs{FTS5: retrieval.NewLeg(index), Vector: vectorLeg, Filter: filter}
}

// loadEvalFixtures loads the committed corpus, both query sets, and the
// recorded embeddings, failing the test on any load error.
func loadEvalFixtures(t *testing.T) (eval.EvalCorpus, eval.QuerySet, eval.QuerySet, eval.RecordedEmbeddings) {
	t.Helper()
	fixtureCorpus := loadRealCorpus(t)
	knownItem := loadQuerySet(t, "testdata/known-item-queries.jsonl", "known-item", fixtureCorpus)
	semantic := loadQuerySet(t, "testdata/semantic-paraphrase-queries.jsonl", "semantic-paraphrase", fixtureCorpus)
	embeddings := loadRecordedEmbeddings(t)
	return fixtureCorpus, knownItem, semantic, embeddings
}

func loadQuerySet(t *testing.T, path, name string, c eval.EvalCorpus) eval.QuerySet {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	qs, err := eval.LoadQuerySet(name, f, c)
	if err != nil {
		t.Fatalf("LoadQuerySet(%s): %v", name, err)
	}
	return qs
}

func loadRecordedEmbeddings(t *testing.T) eval.RecordedEmbeddings {
	t.Helper()
	f, err := os.Open("testdata/recorded/real-embedder.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	rec, err := eval.LoadRecordedEmbeddings(f)
	if err != nil {
		t.Fatalf("LoadRecordedEmbeddings: %v", err)
	}
	return rec
}

func TestHarness_KnownItemFindsItsOwnDocument(t *testing.T) {
	fixtureCorpus, knownItem, _, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)

	results, err := eval.RunKnownItem(context.Background(), knownItem, legs)
	if err != nil {
		t.Fatalf("RunKnownItem: %v", err)
	}
	if len(results) != len(knownItem.Queries) {
		t.Fatalf("got %d results for %d queries", len(results), len(knownItem.Queries))
	}
	recall, err := eval.RecallAt10(knownItem.Queries, fts5OnlyResults(results))
	if err != nil {
		t.Fatalf("RecallAt10: %v", err)
	}
	// Every known-item query is a distinctive excerpt of its own document
	// (v1's gen-eval-corpus.py method), so a real FTS5 index over the
	// real corpus text must find it: a recall below 1.0 here means the
	// harness itself is wired wrong, not that retrieval is imperfect.
	if recall < 1.0 {
		t.Fatalf("known-item FTS5-only recall@10 = %v, want 1.0 (distinctive excerpts of the corpus's own documents)", recall)
	}
}

func TestHarness_SemanticParaphraseRuns(t *testing.T) {
	fixtureCorpus, _, semantic, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)

	results, err := eval.RunSemanticParaphrase(context.Background(), semantic, legs)
	if err != nil {
		t.Fatalf("RunSemanticParaphrase: %v", err)
	}
	if len(results) != len(semantic.Queries) {
		t.Fatalf("got %d results for %d queries", len(results), len(semantic.Queries))
	}
	for i, r := range results {
		if len(r.Fused.RankedIDs) == 0 {
			t.Errorf("query %q: fused ranking is empty", semantic.Queries[i].Text)
		}
	}
}

func TestHarness_NoVectorLegDegradesToFTS5Only(t *testing.T) {
	fixtureCorpus, knownItem, _, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)
	legs.Vector = nil

	results, err := eval.RunKnownItem(context.Background(), knownItem, legs)
	if err != nil {
		t.Fatalf("RunKnownItem: %v", err)
	}
	for i, r := range results {
		if len(r.Fused.RankedIDs) != len(r.FTS5Only.RankedIDs) {
			t.Errorf("query %q: fused/fts5-only ranking length differs with no vector leg: %d vs %d",
				knownItem.Queries[i].Text, len(r.Fused.RankedIDs), len(r.FTS5Only.RankedIDs))
		}
	}
}

func fts5OnlyResults(results []eval.RunResult) []eval.RankedResult {
	out := make([]eval.RankedResult, len(results))
	for i, r := range results {
		out[i] = r.FTS5Only
	}
	return out
}
