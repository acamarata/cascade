package eval_test

// Purpose: TestTypes — the typed constructors' fail-closed behavior:
//   empty sets, duplicate ids, absent expected ids, dimension mismatch,
//   and non-finite vectors.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — no clock, no filesystem, no network.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/eval"
)

func mustCorpus(t *testing.T) eval.EvalCorpus {
	t.Helper()
	c, err := eval.NewEvalCorpus([]eval.Document{{ID: "a", Text: "alpha"}, {ID: "b", Text: "beta"}})
	if err != nil {
		t.Fatalf("NewEvalCorpus: %v", err)
	}
	return c
}

func TestNewEvalCorpus_Empty(t *testing.T) {
	if _, err := eval.NewEvalCorpus(nil); err == nil {
		t.Fatal("empty corpus: want error")
	}
}

func TestNewEvalCorpus_DuplicateID(t *testing.T) {
	_, err := eval.NewEvalCorpus([]eval.Document{{ID: "a", Text: "x"}, {ID: "a", Text: "y"}})
	if err == nil {
		t.Fatal("duplicate id: want error")
	}
}

func TestNewEvalCorpus_EmptyFields(t *testing.T) {
	if _, err := eval.NewEvalCorpus([]eval.Document{{ID: "", Text: "x"}}); err == nil {
		t.Fatal("empty id: want error")
	}
	if _, err := eval.NewEvalCorpus([]eval.Document{{ID: "a", Text: ""}}); err == nil {
		t.Fatal("empty text: want error")
	}
}

func TestNewQuerySet_Empty(t *testing.T) {
	if _, err := eval.NewQuerySet("s", nil, mustCorpus(t)); err == nil {
		t.Fatal("empty query set: want error")
	}
}

func TestNewQuerySet_AbsentExpectedID(t *testing.T) {
	_, err := eval.NewQuerySet("s", []eval.Query{{Text: "q", ExpectedIDs: []string{"missing"}}}, mustCorpus(t))
	if err == nil {
		t.Fatal("absent expected id: want error")
	}
}

func TestNewQuerySet_EmptyQueryFields(t *testing.T) {
	c := mustCorpus(t)
	if _, err := eval.NewQuerySet("s", []eval.Query{{Text: "", ExpectedIDs: []string{"a"}}}, c); err == nil {
		t.Fatal("empty query text: want error")
	}
	if _, err := eval.NewQuerySet("s", []eval.Query{{Text: "q", ExpectedIDs: nil}}, c); err == nil {
		t.Fatal("no expected ids: want error")
	}
}

func TestNewQuerySet_Valid(t *testing.T) {
	qs, err := eval.NewQuerySet("s", []eval.Query{{Text: "q", ExpectedIDs: []string{"a"}}}, mustCorpus(t))
	if err != nil {
		t.Fatalf("NewQuerySet: %v", err)
	}
	if qs.Name != "s" || len(qs.Queries) != 1 {
		t.Fatalf("got %+v", qs)
	}
}

func validRecords() []eval.EmbeddingRecord {
	return []eval.EmbeddingRecord{
		{Text: "a", Vector: []float32{1, 2}},
		{Text: "b", Vector: []float32{3, 4}},
	}
}

func validProvenance() eval.Provenance {
	return eval.Provenance{Tool: "tool", Version: "1.0.0", Date: "2026-09-06"}
}

func TestNewRecordedEmbeddings_MissingProvenance(t *testing.T) {
	if _, err := eval.NewRecordedEmbeddings(eval.Provenance{}, validRecords()); err == nil {
		t.Fatal("missing provenance: want error")
	}
	if _, err := eval.NewRecordedEmbeddings(eval.Provenance{Tool: "t"}, validRecords()); err == nil {
		t.Fatal("partial provenance: want error")
	}
}

func TestNewRecordedEmbeddings_Empty(t *testing.T) {
	if _, err := eval.NewRecordedEmbeddings(validProvenance(), nil); err == nil {
		t.Fatal("empty records: want error")
	}
}

func TestNewRecordedEmbeddings_DimensionMismatch(t *testing.T) {
	records := []eval.EmbeddingRecord{{Text: "a", Vector: []float32{1, 2}}, {Text: "b", Vector: []float32{1}}}
	if _, err := eval.NewRecordedEmbeddings(validProvenance(), records); err == nil {
		t.Fatal("dimension mismatch: want error")
	}
}

func TestNewRecordedEmbeddings_NonFinite(t *testing.T) {
	for _, bad := range [][]float32{
		{float32(nan()), 1},
		{float32(inf()), 1},
	} {
		records := []eval.EmbeddingRecord{{Text: "a", Vector: bad}}
		if _, err := eval.NewRecordedEmbeddings(validProvenance(), records); err == nil {
			t.Fatalf("non-finite vector %v: want error", bad)
		}
	}
}

func TestNewRecordedEmbeddings_DuplicateText(t *testing.T) {
	records := []eval.EmbeddingRecord{{Text: "a", Vector: []float32{1}}, {Text: "a", Vector: []float32{2}}}
	if _, err := eval.NewRecordedEmbeddings(validProvenance(), records); err == nil {
		t.Fatal("duplicate text: want error")
	}
}

func TestNewRecordedEmbeddings_LookupRoundTrip(t *testing.T) {
	rec, err := eval.NewRecordedEmbeddings(validProvenance(), validRecords())
	if err != nil {
		t.Fatalf("NewRecordedEmbeddings: %v", err)
	}
	v, ok := rec.Lookup("a")
	if !ok || len(v) != 2 {
		t.Fatalf("Lookup(a) = %v, %v", v, ok)
	}
	if _, ok := rec.Lookup("missing"); ok {
		t.Fatal("Lookup(missing): want not found")
	}
}

func nan() float64 { var z float64; return z / z }
func inf() float64 { var z float64; return 1 / z }
