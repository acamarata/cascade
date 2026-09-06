package eval_test

// Purpose: fixture-decoder tests, including TestRecordedRealEmbedderFixture
//   (Art.2 real-counterpart verification of the committed fixture's
//   provenance and shape).
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — reads only the committed testdata/, no network.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/retrieval/eval"
)

func TestLoadCorpus_MalformedJSON(t *testing.T) {
	if _, err := eval.LoadCorpus(strings.NewReader("not json\n")); err == nil {
		t.Fatal("malformed JSONL: want error")
	}
}

func TestLoadCorpus_Real(t *testing.T) {
	f, err := os.Open("testdata/corpus.jsonl")
	if err != nil {
		t.Fatalf("open corpus.jsonl: %v", err)
	}
	defer f.Close()
	c, err := eval.LoadCorpus(f)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(c.Documents) != 6 {
		t.Fatalf("got %d documents, want 6", len(c.Documents))
	}
}

func loadRealCorpus(t *testing.T) eval.EvalCorpus {
	t.Helper()
	f, err := os.Open("testdata/corpus.jsonl")
	if err != nil {
		t.Fatalf("open corpus.jsonl: %v", err)
	}
	defer f.Close()
	c, err := eval.LoadCorpus(f)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	return c
}

func TestLoadQuerySet_MalformedJSON(t *testing.T) {
	if _, err := eval.LoadQuerySet("s", strings.NewReader("{not json}\n"), loadRealCorpus(t)); err == nil {
		t.Fatal("malformed JSONL: want error")
	}
}

func TestLoadQuerySet_KnownItemReal(t *testing.T) {
	corpus := loadRealCorpus(t)
	f, err := os.Open("testdata/known-item-queries.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	qs, err := eval.LoadQuerySet("known-item", f, corpus)
	if err != nil {
		t.Fatalf("LoadQuerySet: %v", err)
	}
	if len(qs.Queries) != 18 {
		t.Fatalf("got %d known-item queries, want 18", len(qs.Queries))
	}
}

func TestLoadQuerySet_SemanticParaphraseReal(t *testing.T) {
	corpus := loadRealCorpus(t)
	f, err := os.Open("testdata/semantic-paraphrase-queries.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	qs, err := eval.LoadQuerySet("semantic-paraphrase", f, corpus)
	if err != nil {
		t.Fatalf("LoadQuerySet: %v", err)
	}
	if len(qs.Queries) != 6 {
		t.Fatalf("got %d semantic/paraphrase queries, want 6", len(qs.Queries))
	}
}

func TestLoadRecordedEmbeddings_MalformedJSON(t *testing.T) {
	if _, err := eval.LoadRecordedEmbeddings(strings.NewReader("{not json")); err == nil {
		t.Fatal("malformed JSON: want error")
	}
}

// TestRecordedRealEmbedderFixture is this ticket's Art.2 real-counterpart
// check on the committed recording itself: it must load, its provenance
// must be stated, and it must cover every text the harness's own fixtures
// query the embedder with.
func TestRecordedRealEmbedderFixture(t *testing.T) {
	f, err := os.Open("testdata/recorded/real-embedder.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	rec, err := eval.LoadRecordedEmbeddings(f)
	if err != nil {
		t.Fatalf("LoadRecordedEmbeddings: %v", err)
	}
	if !rec.Provenance.Valid() {
		t.Fatal("recorded embeddings: provenance not stated")
	}
	if rec.Dimensions == 0 {
		t.Fatal("recorded embeddings: zero dimensions")
	}
	corpus := loadRealCorpus(t)
	for _, d := range corpus.Documents {
		if _, ok := rec.Lookup(d.Text); !ok {
			t.Errorf("no recorded vector for corpus document %q", d.ID)
		}
	}
	for _, name := range []string{"testdata/known-item-queries.jsonl", "testdata/semantic-paraphrase-queries.jsonl"} {
		qf, err := os.Open(name)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		qs, err := eval.LoadQuerySet(name, qf, corpus)
		qf.Close()
		if err != nil {
			t.Fatalf("LoadQuerySet(%s): %v", name, err)
		}
		for _, q := range qs.Queries {
			if _, ok := rec.Lookup(q.Text); !ok {
				t.Errorf("no recorded vector for query %q from %s", q.Text, name)
			}
		}
	}
}

func TestLoadBaseline_MalformedJSON(t *testing.T) {
	if _, err := eval.LoadBaseline(strings.NewReader("{not json")); err == nil {
		t.Fatal("malformed JSON: want error")
	}
}

func TestLoadBaseline_MissingProvenance(t *testing.T) {
	body := `{"metric":"mrr_at_10","fts5_only":0.9,"rrf_full":0.9}`
	if _, err := eval.LoadBaseline(strings.NewReader(body)); err == nil {
		t.Fatal("missing provenance: want error")
	}
}

func TestLoadBaseline_Real(t *testing.T) {
	f, err := os.Open("testdata/v1-goldens/august-2026-baseline.json")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	b, err := eval.LoadBaseline(f)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if !b.Provenance.Valid() {
		t.Fatal("baseline: provenance not stated")
	}
	if b.FTS5Only <= 0 || b.RRFFull <= 0 {
		t.Fatalf("baseline: non-positive values %+v", b)
	}
}

// evalPackageImportPath is this package's module-relative import path,
// the key it is registered under in coverage-baseline.json.
const evalPackageImportPath = "internal/retrieval/eval"

// TestCoverageFloor85Statements80Branches is this ticket's contract-named
// coverage-registration check. It asserts what can honestly be asserted:
// this package is classified into the Art.4 core-engine tier (>=85%
// statements) and is registered in the committed baseline ratchet at or
// above that floor. It does NOT assert an 80% BRANCHES number:
// internal/build/coveragegate.go's own package doc records, as a
// deliberate named gap rather than a silent omission, that Go's coverage
// tooling measures statements only — there is no branch-coverage mode and
// this repo adopts no separate tool for one. Asserting a branch
// percentage no tool measured would be exactly the "gate that reads as
// coverage but enforces nothing" failure this ticket exists to avoid
// (AGENT-BRIEF: follow the tree, quote the contradiction).
func TestCoverageFloor85Statements80Branches(t *testing.T) {
	tier, floor, ok := build.PackageTier(evalPackageImportPath)
	if !ok {
		t.Fatalf("PackageTier(%q) reported not covered by Art.4 at all", evalPackageImportPath)
	}
	if tier != build.TierCore {
		t.Fatalf("PackageTier(%q) = %v, want TierCore", evalPackageImportPath, tier)
	}
	if floor < 85.0 {
		t.Fatalf("PackageTier(%q) floor = %v, want >= 85.0", evalPackageImportPath, floor)
	}

	data, err := os.ReadFile("../../build/testdata/coverage-baseline.json")
	if err != nil {
		t.Fatalf("reading coverage-baseline.json: %v", err)
	}
	baseline, err := build.ParseBaseline(data)
	if err != nil {
		t.Fatalf("ParseBaseline: %v", err)
	}
	entry, present := baseline[evalPackageImportPath]
	if !present {
		t.Fatalf("%q is not registered in the A-T8 coverage-baseline.json ratchet", evalPackageImportPath)
	}
	if entry.Tier != string(build.TierCore) {
		t.Errorf("baseline entry tier = %q, want %q", entry.Tier, build.TierCore)
	}
	if entry.Floor < 85.0 || entry.Baseline < 85.0 {
		t.Errorf("baseline entry %+v does not meet the >=85%% statements floor", entry)
	}
	// Branches: not measurable with this repo's tooling — see the doc
	// comment above. Recorded, not silently skipped.
	t.Log("branch coverage: not measurable with this repo's tooling (internal/build/coveragegate.go); statements-only floor asserted above")
}
