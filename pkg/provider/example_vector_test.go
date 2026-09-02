// Purpose: a runnable godoc Example for provider.VectorStore (Art.10.6),
//   backed by a minimal in-memory double (see example_store_test.go for the
//   package-level "why a local double, not storetest" rationale).
// SPORT: pkg.provider.VectorStore/ADDED (P1-E02-W1-S02-T1 CR follow-up).

package provider_test

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
)

// exampleVectorStore is a real, minimal namespace-scoped vector index: it
// ranks matches by plain dot-product score (no normalization) and applies
// exact-equality Filter matching. It is a demonstration double, not a
// production similarity engine.
type exampleVectorStore struct {
	mu   sync.Mutex
	data map[string]map[string]provider.Vector
}

func newExampleVectorStore() *exampleVectorStore {
	return &exampleVectorStore{data: make(map[string]map[string]provider.Vector)}
}

func (v *exampleVectorStore) Upsert(_ context.Context, namespace string, vectors []provider.Vector) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.data[namespace] == nil {
		v.data[namespace] = make(map[string]provider.Vector)
	}
	for _, vec := range vectors {
		v.data[namespace][vec.ID] = vec
	}
	return nil
}

func (v *exampleVectorStore) Query(_ context.Context, namespace string, req provider.VectorQuery) ([]provider.VectorMatch, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	matches := make([]provider.VectorMatch, 0, len(v.data[namespace]))
	for _, vec := range v.data[namespace] {
		if !matchesFilter(vec.Metadata, req.Filter) {
			continue
		}
		matches = append(matches, provider.VectorMatch{ID: vec.ID, Score: dot(vec.Values, req.Values), Metadata: vec.Metadata})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if req.TopK > 0 && len(matches) > req.TopK {
		matches = matches[:req.TopK]
	}
	return matches, nil
}

func (v *exampleVectorStore) Delete(_ context.Context, namespace string, ids []string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, id := range ids {
		delete(v.data[namespace], id)
	}
	return nil
}

func (v *exampleVectorStore) Count(_ context.Context, namespace string) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.data[namespace]), nil
}

func (v *exampleVectorStore) Namespaces(_ context.Context) ([]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.data))
	for ns := range v.data {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out, nil
}

func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		if i >= len(b) {
			break
		}
		sum += a[i] * b[i]
	}
	return sum
}

func matchesFilter(metadata, filter map[string]any) bool {
	for k, want := range filter {
		if metadata[k] != want {
			return false
		}
	}
	return true
}

// ExampleVectorStore demonstrates Upsert, a filtered top-K Query, and
// Count against the R-14.4 canonical namespace-scoped surface.
func ExampleVectorStore() {
	ctx := context.Background()
	var vs provider.VectorStore = newExampleVectorStore()

	err := vs.Upsert(ctx, "docs", []provider.Vector{
		{ID: "en-doc", Values: []float32{1, 0}, Metadata: map[string]any{"lang": "en"}},
		{ID: "fr-doc", Values: []float32{0, 1}, Metadata: map[string]any{"lang": "fr"}},
	})
	if err != nil {
		fmt.Println("upsert error:", err)
		return
	}

	matches, err := vs.Query(ctx, "docs", provider.VectorQuery{
		Values: []float32{1, 0},
		TopK:   1,
		Filter: map[string]any{"lang": "en"},
	})
	if err != nil {
		fmt.Println("query error:", err)
		return
	}
	fmt.Println(matches[0].ID)

	count, err := vs.Count(ctx, "docs")
	if err != nil {
		fmt.Println("count error:", err)
		return
	}
	fmt.Println(count)

	// Output:
	// en-doc
	// 2
}
