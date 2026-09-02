// Purpose: FlatVectorStore semantics tests: R-14.4 namespace isolation,
//   the cosine-vs-raw-dot-product magnitude-invariance proof, zero-
//   magnitude-vector handling, and req.Filter exact-equality matching. See
//   vector_test.go for package-level constraints (Art.10.2 test-file
//   exemption, Art.7.1).
// SPORT: providers.localvector.FlatVectorStore/ADDED (P1-E02-W1-S03-T4).

package localvector_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestQuery_NamespaceIsolation proves R-14.4's namespace scoping: a vector
// upserted into one namespace must never appear in another namespace's
// Query results, even when both namespaces share the same embedding
// dimension and even when an ID collides across namespaces.
func TestQuery_NamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, "ns-a", []provider.Vector{{ID: "shared-id", Values: []float32{1, 0}}}); err != nil {
		t.Fatalf("Upsert ns-a: %v", err)
	}
	if err := vs.Upsert(ctx, "ns-b", []provider.Vector{{ID: "shared-id", Values: []float32{0, 1}}}); err != nil {
		t.Fatalf("Upsert ns-b: %v", err)
	}

	matches, err := vs.Query(ctx, "ns-a", provider.VectorQuery{Values: []float32{1, 0}, TopK: 5})
	if err != nil {
		t.Fatalf("Query ns-a: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "shared-id" {
		t.Fatalf("Query ns-a = %+v, want exactly ns-a's own shared-id", matches)
	}
	nb, err := vs.Count(ctx, "ns-b")
	if err != nil || nb != 1 {
		t.Fatalf("Count ns-b = (%d, %v), want (1, nil)", nb, err)
	}
	// ns-b's shared-id record is [0,1], not ns-a's [1,0] — querying ns-b
	// with ns-a's own vector must score it as an exact match (cosine 1.0),
	// proving ns-b stores its own distinct record under the colliding ID
	// rather than having been overwritten or merged by ns-a's Upsert.
	mb, err := vs.Query(ctx, "ns-b", provider.VectorQuery{Values: []float32{0, 1}, TopK: 5})
	if err != nil {
		t.Fatalf("Query ns-b: %v", err)
	}
	if len(mb) != 1 || mb[0].ID != "shared-id" || mb[0].Score != 1 {
		t.Fatalf("Query ns-b = %+v, want exactly one exact-match shared-id record (proves no cross-namespace merge)", mb)
	}
	if err := vs.Delete(ctx, "ns-a", []string{"shared-id"}); err != nil {
		t.Fatalf("Delete ns-a shared-id: %v", err)
	}
	nb2, err := vs.Count(ctx, "ns-b")
	if err != nil || nb2 != 1 {
		t.Fatalf("Count ns-b after deleting ns-a's shared-id = (%d, %v), want (1, nil) — delete must not leak across namespaces", nb2, err)
	}
}

// TestQuery_CosineIsMagnitudeInvariant proves FlatVectorStore ranks by
// cosine similarity (angle only), not by raw dot product (angle AND
// magnitude): a same-direction, unit-scale match must outrank a
// same-direction-as-a-worse-candidate vector inflated to a huge magnitude,
// even though the inflated vector would win under a raw dot product.
func TestQuery_CosineIsMagnitudeInvariant(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	query := []float32{1, 0}
	err := vs.Upsert(ctx, "ns", []provider.Vector{
		{ID: "exact", Values: []float32{1, 0}},          // cosine 1.0, dot 1.0
		{ID: "off-axis", Values: []float32{1, 1}},       // cosine ~0.707, dot 1.0
		{ID: "off-axis-big", Values: []float32{10, 10}}, // cosine ~0.707 (same as off-axis), dot 10.0
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	matches, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: query, TopK: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("Query returned %d matches, want 3", len(matches))
	}
	// Under raw dot product, off-axis-big (dot=10) would rank above exact
	// (dot=1). Under cosine similarity, exact must rank first because its
	// angle to query is 0.
	if matches[0].ID != "exact" {
		t.Fatalf("Query top match = %q, want %q (cosine must be magnitude-invariant, not raw dot product)", matches[0].ID, "exact")
	}
	if matches[0].Score <= matches[1].Score {
		t.Fatalf("exact's score %v must exceed the off-axis pair's score %v", matches[0].Score, matches[1].Score)
	}
	// off-axis and off-axis-big share direction, so they must tie in
	// score despite the 10x magnitude difference — proof normalization is
	// actually happening, not just monotonic with dot product.
	if matches[1].Score != matches[2].Score {
		t.Fatalf("off-axis and off-axis-big scores = %v, %v, want equal (same direction, different magnitude)", matches[1].Score, matches[2].Score)
	}
	// Deterministic tie-break: ascending ID.
	if matches[1].ID != "off-axis" || matches[2].ID != "off-axis-big" {
		t.Fatalf("tied pair order = %q, %q, want ascending-ID tie-break (off-axis, off-axis-big)", matches[1].ID, matches[2].ID)
	}
}

// TestQuery_ZeroMagnitudeVectorScoresZeroNotNaN proves a zero vector
// (undefined direction) never produces NaN/Inf in a ranking — it scores 0
// and sorts last rather than corrupting the sort order.
func TestQuery_ZeroMagnitudeVectorScoresZeroNotNaN(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	err := vs.Upsert(ctx, "ns", []provider.Vector{
		{ID: "zero", Values: []float32{0, 0}},
		{ID: "real", Values: []float32{1, 0}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	matches, err := vs.Query(ctx, "ns", provider.VectorQuery{Values: []float32{1, 0}, TopK: 2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("Query returned %d matches, want 2", len(matches))
	}
	if matches[0].ID != "real" {
		t.Fatalf("top match = %q, want %q", matches[0].ID, "real")
	}
	if matches[1].ID != "zero" || matches[1].Score != 0 {
		t.Fatalf("second match = %+v, want {ID: zero, Score: 0}", matches[1])
	}
}

// TestQuery_FilterExactEquality proves req.Filter restricts matches to
// vectors whose Metadata satisfies every key/value pair exactly.
func TestQuery_FilterExactEquality(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	err := vs.Upsert(ctx, "ns", []provider.Vector{
		{ID: "en", Values: []float32{1, 0}, Metadata: map[string]any{"lang": "en"}},
		{ID: "fr", Values: []float32{1, 0}, Metadata: map[string]any{"lang": "fr"}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	matches, err := vs.Query(ctx, "ns", provider.VectorQuery{
		Values: []float32{1, 0}, TopK: 5, Filter: map[string]any{"lang": "en"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "en" {
		t.Fatalf("filtered Query = %+v, want exactly [en]", matches)
	}
}
