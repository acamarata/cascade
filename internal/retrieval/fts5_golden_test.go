package retrieval_test

// Purpose: TestGoldenFTS5 — the ranking-quality golden. It is a
//   KNOWN-ITEM retrieval test: each query is a distinctive passage lifted
//   out of one document, so the correct answer is the document it came
//   from, and the expectation is the SOURCE PATH rather than any engine's
//   recorded output.
//
//   Art.2 provenance: the queries and the ground truth come from the
//   archived v1 repository's own evaluation dataset (generated there by
//   scripts/gen-eval-corpus.py from its 89 public documents, commit
//   recorded in the fixture), and the corpus documents are that repo's own
//   files copied verbatim. Nothing in the fixture is this
//   implementation's output, so the golden cannot pass by agreeing with
//   itself, and v1's own ranker is not consulted either: v1 is read-only
//   reference (ARCHIVE-MAP) and its Rust FTS5 output has no reusable Go
//   golden form, exactly as recorded for the chunk goldens next door.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — real driver, TempDir, no clock, no network.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
)

// goldenFTSPath is the harvested fixture (06 §5 rule 12 canonical path).
const goldenFTSPath = "testdata/v1-goldens/fts5_queries.json"

// goldenFTS is the fixture's shape.
type goldenFTS struct {
	Provenance map[string]string `json:"provenance"`
	Documents  []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"documents"`
	Queries []struct {
		Query         string `json:"query"`
		ExpectTopPath string `json:"expect_top_path"`
	} `json:"queries"`
}

// loadGoldenFTS reads the fixture, refusing one that has lost its
// provenance: a golden with no stated source is a golden nobody can check.
func loadGoldenFTS(t *testing.T) goldenFTS {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(goldenFTSPath))
	if err != nil {
		t.Fatalf("reading %s: %v", goldenFTSPath, err)
	}
	var g goldenFTS
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("decoding %s: %v", goldenFTSPath, err)
	}
	for _, key := range []string{"source_repo", "source_commit", "queries_from", "method", "harvested"} {
		if g.Provenance[key] == "" {
			t.Fatalf("%s has no %s in its provenance", goldenFTSPath, key)
		}
	}
	if len(g.Documents) < 2 || len(g.Queries) == 0 {
		t.Fatalf("%s holds %d documents and %d queries; a single-document corpus "+
			"would make every ranking assertion vacuous", goldenFTSPath, len(g.Documents), len(g.Queries))
	}
	return g
}

// TestGoldenFTS5 indexes the harvested corpus and asserts that every
// harvested query ranks its own source document first.
func TestGoldenFTS5(t *testing.T) {
	g := loadGoldenFTS(t)
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	byPath := make(map[string]string, len(g.Documents))
	for _, doc := range g.Documents {
		h.indexDoc(t, ftsHandbook, doc.Path, doc.Content)
		byPath[doc.Path] = doc.Content
	}

	for _, q := range g.Queries {
		if byPath[q.ExpectTopPath] == "" {
			t.Errorf("query %q expects %q, which is not in the fixture's corpus",
				q.Query, q.ExpectTopPath)
			continue
		}
		hits := h.search(t, ftsSession, q.Query, len(g.Documents))
		if len(hits) == 0 {
			t.Errorf("query %q returned nothing; its own source document carries every term",
				q.Query)
			continue
		}
		if hits[0].Path != q.ExpectTopPath {
			t.Errorf("query %q ranked %q first, want %q (the document the passage was lifted from); full order %v",
				q.Query, hits[0].Path, q.ExpectTopPath, paths(hits))
		}
	}
}

// TestGoldenFTS5IsDeterministic: the golden corpus ranks identically
// across two independently built indexes, which is the property the
// fixture would otherwise only assert one run at a time.
func TestGoldenFTS5IsDeterministic(t *testing.T) {
	g := loadGoldenFTS(t)
	build := func() []retrieval.Hit {
		h := newFTSHarness(t)
		h.addCorpus(t, ftsHandbook)
		for _, doc := range g.Documents {
			h.indexDoc(t, ftsHandbook, doc.Path, doc.Content)
		}
		return h.search(t, ftsSession, g.Queries[0].Query, len(g.Documents))
	}
	first, second := build(), build()
	if len(first) != len(second) {
		t.Fatalf("two builds returned %d and %d hits", len(first), len(second))
	}
	for i := range first {
		if first[i].ChunkID != second[i].ChunkID || first[i].Score != second[i].Score {
			t.Errorf("rank %d differs between builds: %+v vs %+v", i+1, first[i], second[i])
		}
	}
}
