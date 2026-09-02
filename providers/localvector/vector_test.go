// Purpose: FlatVectorStore's conformance and recall@k tests: TestStoreVector
//   (the R-14.4 storetest-vector suite) and TestRecallAtK (the exact-recall
//   fixture proof). Driver-specific error-path tests live in
//   errors_test.go; namespace-isolation/metric/filter semantics tests live
//   in semantics_test.go; store-failure-injection coverage tests live in
//   faults_test.go — split across sibling files to satisfy Art.10.3's
//   300-line cap (R-14.117 authorizes in-package splits of a file a ticket
//   owns).
// Constraints: test files may import internal/** (Art.10.2 exempts
//   _test.go) — this file imports internal/storage/storetest for both the
//   conformance suite and its NewMemStore reference Store. Every test
//   writes nothing to disk (Art.7.1 is satisfied vacuously: MemStore is
//   pure in-memory).
// SPORT: providers.localvector.FlatVectorStore/ADDED (P1-E02-W1-S03-T4).

package localvector_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

func newStore(t *testing.T) provider.VectorStore {
	t.Helper()
	return localvector.New(storetest.NewMemStore())
}

// TestStoreVector runs the full R-14.4 storetest-vector conformance suite
// against FlatVectorStore backed by storetest.NewMemStore.
func TestStoreVector(t *testing.T) {
	storetest.RunVectorStoreTests(t, func(t *testing.T) provider.VectorStore {
		t.Helper()
		return newStore(t)
	})
}

// --- recall@k fixture -------------------------------------------------

type fixtureVector struct {
	ID     string    `json:"id"`
	Values []float32 `json:"values"`
}

type fixtureQuery struct {
	ID            string    `json:"id"`
	Values        []float32 `json:"values"`
	ExpectedTop10 []string  `json:"expected_top10"`
}

type fixture struct {
	Seed    int64           `json:"seed"`
	Dim     int             `json:"dim"`
	Vectors []fixtureVector `json:"vectors"`
	Queries []fixtureQuery  `json:"queries"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/recall_fixture.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Vectors) < 50 {
		t.Fatalf("fixture has %d vectors, want >= 50", len(fx.Vectors))
	}
	return fx
}

// idSet returns m's IDs as a set, for order-independent recall
// comparison.
func idSet(matches []provider.VectorMatch) map[string]bool {
	out := make(map[string]bool, len(matches))
	for _, m := range matches {
		out[m.ID] = true
	}
	return out
}

func idSlice(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func idsEqual(got []provider.VectorMatch, wantIDs []string) bool {
	if len(got) != len(wantIDs) {
		return false
	}
	g, w := idSet(got), idSlice(wantIDs)
	if len(g) != len(w) {
		return false
	}
	for id := range w {
		if !g[id] {
			return false
		}
	}
	return true
}

func orderEqual(got []provider.VectorMatch, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].ID != want[i] {
			return false
		}
	}
	return true
}

func matchIDs(matches []provider.VectorMatch) []string {
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.ID
	}
	return out
}

// recallHit runs one query at TopK=k against vs, checks the returned IDs
// against want (both as a set — recall — and in order — brute force is
// exact, so ranked order must match too), logs any mismatch via t.Errorf,
// and reports whether this query counted as a recall@k hit.
func recallHit(t *testing.T, vs provider.VectorStore, q fixtureQuery, k int, want []string) bool {
	t.Helper()
	ctx := context.Background()
	got, err := vs.Query(ctx, "corpus", provider.VectorQuery{Values: q.Values, TopK: k})
	if err != nil {
		t.Fatalf("Query(%s, TopK=%d): %v", q.ID, k, err)
	}
	hit := idsEqual(got, want)
	if !hit {
		t.Errorf("recall@%d miss for %s: got %v, want set %v", k, q.ID, matchIDs(got), want)
	}
	if !orderEqual(got, want) {
		t.Errorf("recall@%d order mismatch for %s: got %v, want %v", k, q.ID, matchIDs(got), want)
	}
	return hit
}

// TestRecallAtK asserts recall@5 == 1.0 and recall@10 == 1.0 against
// testdata/recall_fixture.json's brute-force ground truth. Brute-force
// cosine scan is an EXACT algorithm, so any miss here is a driver bug, not
// an approximation tolerance — see testdata/README.md for the fixture's
// generation method and why its recall boundaries carry no ties.
func TestRecallAtK(t *testing.T) {
	fx := loadFixture(t)
	ctx := context.Background()
	vs := newStore(t)

	vectors := make([]provider.Vector, len(fx.Vectors))
	for i, v := range fx.Vectors {
		vectors[i] = provider.Vector{ID: v.ID, Values: v.Values}
	}
	if err := vs.Upsert(ctx, "corpus", vectors); err != nil {
		t.Fatalf("Upsert corpus: %v", err)
	}

	var hits5, hits10, total int
	for _, q := range fx.Queries {
		total++
		if recallHit(t, vs, q, 5, q.ExpectedTop10[:5]) {
			hits5++
		}
		if recallHit(t, vs, q, 10, q.ExpectedTop10) {
			hits10++
		}
	}

	if total == 0 {
		t.Fatal("fixture has zero queries")
	}
	if recall := float64(hits5) / float64(total); recall != 1.0 {
		t.Errorf("recall@5 = %.4f, want 1.0 (%d/%d queries)", recall, hits5, total)
	}
	if recall := float64(hits10) / float64(total); recall != 1.0 {
		t.Errorf("recall@10 = %.4f, want 1.0 (%d/%d queries)", recall, hits10, total)
	}
}
