// Purpose: FlatVectorStore error-path tests (Article 3's "unit tests incl.
//   ERROR PATHS" requirement) — the cases beyond what storetest's shared
//   RunVectorStoreTests suite exercises: dimension mismatch (both against
//   an already-established namespace and within one Upsert batch), nil/
//   empty embeddings, empty IDs, duplicate-ID replace semantics, zero/
//   negative TopK, empty query Values, unknown-namespace reads, and an
//   emptied namespace's persisted dimensionality. See vector_test.go for
//   package-level constraints (Art.10.2 test-file exemption, Art.7.1).
// SPORT: providers.localvector.FlatVectorStore/ADDED (P1-E02-W1-S03-T4).

package localvector_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestUpsert_DimensionMismatchAgainstNamespace(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "b", Values: []float32{1, 0}}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Upsert dimension mismatch = %v, want KindInvalidInput", err)
	}
	n, cerr := vs.Count(ctx, "ns")
	if cerr != nil || n != 1 {
		t.Fatalf("Count after rejected Upsert = (%d, %v), want (1, nil) — mismatch must not write", n, cerr)
	}
}

func TestUpsert_DimensionMismatchWithinBatch(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	err := vs.Upsert(ctx, "ns", []provider.Vector{
		{ID: "a", Values: []float32{1, 0}},
		{ID: "b", Values: []float32{1, 0, 0}},
	})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Upsert mixed-dimension batch = %v, want KindInvalidInput", err)
	}
}

func TestUpsert_NilOrEmptyEmbedding(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: nil}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Upsert nil embedding = %v, want KindInvalidInput", err)
	}
	err = vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{}}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Upsert empty embedding = %v, want KindInvalidInput", err)
	}
}

func TestUpsert_EmptyID(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "", Values: []float32{1}}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Upsert empty ID = %v, want KindInvalidInput", err)
	}
}

func TestUpsert_DuplicateIDReplaces(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1, 0}, Metadata: map[string]any{"v": 1.0}}}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{0, 1}, Metadata: map[string]any{"v": 2.0}}}); err != nil {
		t.Fatalf("second Upsert (same ID): %v", err)
	}
	n, err := vs.Count(ctx, "ns")
	if err != nil || n != 1 {
		t.Fatalf("Count after duplicate-ID Upsert = (%d, %v), want (1, nil)", n, err)
	}
	matches, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: []float32{0, 1}, TopK: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(matches) != 1 || matches[0].Metadata["v"] != 2.0 {
		t.Fatalf("Query after duplicate-ID Upsert = %+v, want the second Upsert's value to have won", matches)
	}
}

func TestQuery_ZeroOrNegativeTopK(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for _, topK := range []int{0, -1} {
		_, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: []float32{1}, TopK: topK})
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Query TopK=%d = %v, want KindInvalidInput", topK, err)
		}
	}
}

func TestQuery_EmptyValues(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: nil, TopK: 1})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Query nil Values = %v, want KindInvalidInput", err)
	}
}

func TestQuery_DimensionMismatch(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: []float32{1, 0}, TopK: 1})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Query dimension mismatch = %v, want KindInvalidInput", err)
	}
}

func TestQuery_UnknownNamespaceReturnsEmptyNotError(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	matches, err := vs.Query(ctx, "never-touched", provider.VectorQuery{Values: []float32{1}, TopK: 5})
	if err != nil {
		t.Fatalf("Query on unknown namespace = %v, want nil error", err)
	}
	if len(matches) != 0 {
		t.Fatalf("Query on unknown namespace = %v, want empty", matches)
	}
}

func TestCount_UnknownNamespaceIsZero(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	n, err := vs.Count(ctx, "never-touched")
	if err != nil || n != 0 {
		t.Fatalf("Count on unknown namespace = (%d, %v), want (0, nil)", n, err)
	}
}

func TestDelete_NonexistentIsNoop(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Delete(ctx, "never-touched", []string{"absent"}); err != nil {
		t.Fatalf("Delete on unknown namespace/absent id = %v, want nil", err)
	}
}

func TestNamespaces_SurvivesEmptying(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns", []provider.Vector{{ID: "a", Values: []float32{1}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := vs.Delete(ctx, "ns", []string{"a"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := vs.Namespaces(ctx)
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	found := false
	for _, ns := range got {
		if ns == "ns" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Namespaces = %v, want to still include ns after Delete emptied it (established dimensionality persists)", got)
	}
	n, err := vs.Count(ctx, "ns")
	if err != nil || n != 0 {
		t.Fatalf("Count after emptying = (%d, %v), want (0, nil)", n, err)
	}
}
