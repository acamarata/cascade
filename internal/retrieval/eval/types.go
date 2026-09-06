// Package eval is the Epic F retrieval evaluation harness: it runs a
// committed known-item and semantic/paraphrase query set through the real
// S-10.T2 FTS5 leg, the real S-11.T1 vector leg (driven by an offline
// recorded-embedder fixture), and the real S-11.T1 RRF fusion, then
// measures recall@10 and the R-16.9 fusion default gate against those
// measurements.
//
// Purpose: this file declares the harness's value types and the typed,
// fail-closed constructors that validate them (06-FORGE-SPEC.md §5 rule
// 1/2, Art.1 anti-stub). No type here does I/O; fixtures.go owns decoding
// and this file owns shape validation once a value is in memory.
// Inputs: n/a — constructors take already-decoded values.
// Outputs: the validated value, or a *cascade.Error from the frozen
// 14-kind taxonomy (A-T7).
// Constraints: every constructor FAILS CLOSED: an empty set, an absent
// expected id, a dimension mismatch, a non-finite vector, or a missing
// Art.2 provenance field refuses the whole value rather than defaulting
// or dropping the offending entry.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).
package eval

import (
	"math"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Document is one corpus item: a real document identified by its own
// stable, real-world id (its source path in every committed fixture),
// carrying the text both the FTS5 and vector legs index. The id doubles
// as the chunk id the harness assigns it, so a query's ExpectedIDs name
// documents directly rather than a content-addressed hash that would
// change on every rebuild.
type Document struct {
	ID   string
	Text string
}

// EvalCorpus is the committed document set the harness builds a real FTS5
// index and vector store over.
type EvalCorpus struct {
	Documents []Document
}

// NewEvalCorpus validates docs and returns an EvalCorpus. An empty set, a
// document with an empty id or text, or a duplicate id is refused: each
// would make the harness's own accounting of what it indexed silently
// wrong.
func NewEvalCorpus(docs []Document) (EvalCorpus, error) {
	if len(docs) == 0 {
		return EvalCorpus{}, cascade.New(cascade.KindInvalidInput, "eval: corpus has no documents")
	}
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if d.ID == "" {
			return EvalCorpus{}, cascade.New(cascade.KindInvalidInput, "eval: corpus document has an empty id")
		}
		if d.Text == "" {
			return EvalCorpus{}, cascade.Newf(cascade.KindInvalidInput, "eval: corpus document %q has empty text", d.ID)
		}
		if seen[d.ID] {
			return EvalCorpus{}, cascade.Newf(cascade.KindInvalidInput, "eval: duplicate corpus document id %q", d.ID)
		}
		seen[d.ID] = true
	}
	return EvalCorpus{Documents: docs}, nil
}

// IDs returns every document id in the corpus, for expected-id validation.
func (c EvalCorpus) IDs() map[string]bool {
	out := make(map[string]bool, len(c.Documents))
	for _, d := range c.Documents {
		out[d.ID] = true
	}
	return out
}

// Query is one evaluation query: the text to run and the corpus document
// ids that answer it (ground truth), never this harness's own output.
type Query struct {
	Text        string
	ExpectedIDs []string
}

// QuerySet is a named, committed set of queries: known-item or
// semantic/paraphrase.
type QuerySet struct {
	Name    string
	Queries []Query
}

// NewQuerySet validates queries against corpus and returns a QuerySet. An
// empty set, a query with empty text, a query with no expected ids, or an
// expected id absent from corpus is refused.
func NewQuerySet(name string, queries []Query, corpus EvalCorpus) (QuerySet, error) {
	if len(queries) == 0 {
		return QuerySet{}, cascade.Newf(cascade.KindInvalidInput, "eval: query set %q has no queries", name)
	}
	ids := corpus.IDs()
	for _, q := range queries {
		if q.Text == "" {
			return QuerySet{}, cascade.Newf(cascade.KindInvalidInput, "eval: query set %q has a query with empty text", name)
		}
		if len(q.ExpectedIDs) == 0 {
			return QuerySet{}, cascade.Newf(cascade.KindInvalidInput,
				"eval: query %q in set %q has no expected ids", q.Text, name)
		}
		for _, id := range q.ExpectedIDs {
			if !ids[id] {
				return QuerySet{}, cascade.Newf(cascade.KindInvalidInput,
					"eval: query %q in set %q expects id %q, which is not in the corpus", q.Text, name, id)
			}
		}
	}
	return QuerySet{Name: name, Queries: queries}, nil
}

// EmbeddingRecord is one recorded real-embedder output: a text and the
// vector it embedded to.
type EmbeddingRecord struct {
	Text   string
	Vector []float32
}

// Provenance names the real counterpart a fixture was captured from
// (Art.2): the tool, its version, and the capture date. All three are
// required — an unstated field is refused by the loader rather than
// silently accepted as "unknown".
type Provenance struct {
	Tool    string
	Version string
	Date    string
}

// Valid reports whether every provenance field is stated.
func (p Provenance) Valid() bool {
	return p.Tool != "" && p.Version != "" && p.Date != ""
}

// RecordedEmbeddings is a provenance-stamped set of embedding vectors the
// vector leg's Embedder replays offline, never computing one live.
type RecordedEmbeddings struct {
	Provenance Provenance
	Dimensions int
	Records    []EmbeddingRecord
	byText     map[string][]float32
}

// NewRecordedEmbeddings validates records and provenance. A missing
// provenance field, a zero-length vector, a dimension mismatch across
// records, a non-finite vector component, or a duplicate text is refused:
// each would let a malformed recording silently corrupt the vector leg it
// drives.
func NewRecordedEmbeddings(prov Provenance, records []EmbeddingRecord) (RecordedEmbeddings, error) {
	if !prov.Valid() {
		return RecordedEmbeddings{}, cascade.New(cascade.KindIntegrity,
			"eval: recorded embeddings are missing a provenance field (tool/version/date)")
	}
	if len(records) == 0 {
		return RecordedEmbeddings{}, cascade.New(cascade.KindInvalidInput, "eval: recorded embeddings has no records")
	}
	dims := len(records[0].Vector)
	if dims == 0 {
		return RecordedEmbeddings{}, cascade.New(cascade.KindInvalidInput, "eval: recorded embedding has a zero-length vector")
	}
	byText := make(map[string][]float32, len(records))
	for _, r := range records {
		if err := validateEmbeddingRecord(r, dims, byText); err != nil {
			return RecordedEmbeddings{}, err
		}
		byText[r.Text] = r.Vector
	}
	return RecordedEmbeddings{Provenance: prov, Dimensions: dims, Records: records, byText: byText}, nil
}

// validateEmbeddingRecord checks one record against the set's established
// dimensionality and the texts already seen.
func validateEmbeddingRecord(r EmbeddingRecord, dims int, seen map[string][]float32) error {
	if r.Text == "" {
		return cascade.New(cascade.KindInvalidInput, "eval: recorded embedding has empty text")
	}
	if _, dup := seen[r.Text]; dup {
		return cascade.Newf(cascade.KindInvalidInput, "eval: duplicate recorded text %q", r.Text)
	}
	if len(r.Vector) != dims {
		return cascade.Newf(cascade.KindInvalidInput,
			"eval: recorded embedding for %q has %d dimensions, want %d", r.Text, len(r.Vector), dims)
	}
	for _, v := range r.Vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return cascade.Newf(cascade.KindIntegrity,
				"eval: recorded embedding for %q has a non-finite component", r.Text)
		}
	}
	return nil
}

// Lookup returns the recorded vector for text, if any.
func (e RecordedEmbeddings) Lookup(text string) ([]float32, bool) {
	v, ok := e.byText[text]
	return v, ok
}

// Baseline is the harvested v1 August-2026 regression fixture.
//
// It carries v1's own real, unmodified numbers under v1's own metric —
// MRR@10, not recall@10 — because v1's actual eval fixture
// (../cascade-v1/crates/cascade-rag/tests/fixtures/eval/baseline.json)
// never measured recall@10, never split its query set into known-item vs
// semantic/paraphrase, and computed its vector leg through a
// MockEmbedModel rather than a real embedder (v1's own
// retrieval_eval.rs). Relabeling those numbers as this ticket's
// recall@10 fields would misrepresent both the metric and the
// embedder — see testdata/README.md and the ticket journal for the
// contradiction quoted both ways. What DOES transfer honestly across the
// metric and corpus difference is the ratio of fused quality to
// FTS5-only quality: v1's own measurement already shows RRF fusion
// costing known-item quality relative to FTS5-only, and
// TestV1August2026Baseline refuses a REGRESSION of that same ratio,
// which is the one comparison that stays meaningful when the absolute
// scale is not comparable.
type Baseline struct {
	Provenance Provenance
	// Metric names what FTS5Only/RRFFull were measured as ("mrr_at_10").
	Metric string
	// FTS5Only is v1's real fts5_only value.
	FTS5Only float64
	// RRFFull is v1's real rrf_full value (RRF fusion over FTS5 + v1's
	// mock-embedder-backed vector leg).
	RRFFull float64
}

// FusionToFTS5Ratio is RRFFull/FTS5Only: how much of FTS5-only's quality
// RRF fusion retained. A zero FTS5Only is refused by the loader before
// this is ever called.
func (b Baseline) FusionToFTS5Ratio() float64 {
	return b.RRFFull / b.FTS5Only
}

// RankedResult is one query's ranked ids, best first, as one run of the
// harness produced them.
type RankedResult struct {
	QueryText string
	RankedIDs []string
}

// GateVerdict is FusionDefaultGate's measured pass/fail, with the values
// it was computed from, so a caller (the config default, the doctor
// note, a test) can report WHY without recomputing anything.
type GateVerdict struct {
	Pass                bool
	RelativeLift        float64
	KnownItemLossFree   bool
	SemanticFTS5Recall  float64
	SemanticFusedRecall float64
	Detail              string
}
