// Purpose: RunVectorStoreTests — the provider.VectorStore conformance
//   suite (R-14.4 canonical method set).
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// RunVectorStoreTests exercises every provider.VectorStore method against a
// driver produced by newStore.
func RunVectorStoreTests(t *testing.T, newStore VectorStoreFactory) {
	t.Helper()
	t.Run("UpsertQueryDelete", func(t *testing.T) { testVectorUpsertQueryDelete(t, newStore(t)) })
	t.Run("Count", func(t *testing.T) { testVectorCount(t, newStore(t)) })
	t.Run("Namespaces", func(t *testing.T) { testVectorNamespaces(t, newStore(t)) })
	t.Run("DeleteAbsentIsNoop", func(t *testing.T) { testVectorDeleteAbsent(t, newStore(t)) })
	t.Run("QueryTopKCap", func(t *testing.T) { testVectorQueryTopK(t, newStore(t)) })
}

func testVectorUpsertQueryDelete(t *testing.T, v provider.VectorStore) {
	t.Helper()
	ctx := testContext(t)
	vecs := []provider.Vector{
		{ID: "a", Values: []float32{1, 0, 0}, Metadata: map[string]any{"tag": "x"}},
		{ID: "b", Values: []float32{0, 1, 0}, Metadata: map[string]any{"tag": "y"}},
	}
	requireNoError(t, v.Upsert(ctx, "ns", vecs), "Upsert")
	matches, err := v.Query(ctx, "ns", provider.VectorQuery{Values: []float32{1, 0, 0}, TopK: 1})
	requireNoError(t, err, "Query")
	if len(matches) != 1 || matches[0].ID != "a" {
		t.Fatalf("Query = %+v, want top match id=a", matches)
	}
	requireNoError(t, v.Delete(ctx, "ns", []string{"a"}), "Delete")
	n, err := v.Count(ctx, "ns")
	requireNoError(t, err, "Count after Delete")
	if n != 1 {
		t.Fatalf("Count after Delete = %d, want 1", n)
	}
}

func testVectorCount(t *testing.T, v provider.VectorStore) {
	t.Helper()
	ctx := testContext(t)
	n, err := v.Count(ctx, "empty-ns")
	requireNoError(t, err, "Count on empty namespace")
	if n != 0 {
		t.Fatalf("Count on empty namespace = %d, want 0", n)
	}
	err = v.Upsert(ctx, "ns", []provider.Vector{
		{ID: "a", Values: []float32{1}},
		{ID: "b", Values: []float32{2}},
	})
	requireNoError(t, err, "Upsert")
	n, err = v.Count(ctx, "ns")
	requireNoError(t, err, "Count")
	if n != 2 {
		t.Fatalf("Count = %d, want 2", n)
	}
}

func testVectorNamespaces(t *testing.T, v provider.VectorStore) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, v.Upsert(ctx, "ns-a", []provider.Vector{{ID: "1", Values: []float32{1}}}), "Upsert ns-a")
	requireNoError(t, v.Upsert(ctx, "ns-b", []provider.Vector{{ID: "1", Values: []float32{1}}}), "Upsert ns-b")
	got, err := v.Namespaces(ctx)
	requireNoError(t, err, "Namespaces")
	seen := map[string]bool{}
	for _, ns := range got {
		seen[ns] = true
	}
	if !seen["ns-a"] || !seen["ns-b"] {
		t.Fatalf("Namespaces = %v, want to include ns-a and ns-b", got)
	}
}

func testVectorDeleteAbsent(t *testing.T, v provider.VectorStore) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, v.Delete(ctx, "ns", []string{"never-existed"}), "Delete of absent id (want idempotent nil)")
}

func testVectorQueryTopK(t *testing.T, v provider.VectorStore) {
	t.Helper()
	ctx := testContext(t)
	vecs := make([]provider.Vector, 5)
	for i := range vecs {
		vecs[i] = provider.Vector{ID: string(rune('a' + i)), Values: []float32{float32(i)}}
	}
	requireNoError(t, v.Upsert(ctx, "ns", vecs), "Upsert")
	matches, err := v.Query(ctx, "ns", provider.VectorQuery{Values: []float32{0}, TopK: 2})
	requireNoError(t, err, "Query")
	if len(matches) > 2 {
		t.Fatalf("Query returned %d matches, want at most TopK=2", len(matches))
	}
}
