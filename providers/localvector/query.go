// Purpose: FlatVectorStore.Query — the brute-force linear cosine-similarity
//   scan itself. Split out of vector.go to satisfy Art.10.3's 300-line file
//   cap (R-14.117 authorizes in-package splits of a file a ticket owns).
// SPORT: providers.localvector.FlatVectorStore/ADDED (P1-E02-W1-S03-T4).

package localvector

import (
	"context"
	"math"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Query implements provider.VectorStore: a full linear scan of every
// vector in namespace, ranked by descending cosine similarity against
// req.Values, capped at req.TopK.
//
// Error paths, both cascade.KindInvalidInput:
//   - req.TopK <= 0. The embedded provider.VectorQuery godoc only says
//     Query "MUST NOT return more than TopK" results; it does not say
//     whether a non-positive TopK means "no cap" or "no results are
//     wanted". FlatVectorStore resolves that silence by treating TopK <= 0
//     as a caller error rather than guessing an interpretation — a caller
//     that truly wants zero results has no reason to call Query at all.
//   - req.Values is empty, or its length does not match namespace's
//     established dimensionality (readDimension). Comparing a query
//     against vectors of a different length is undefined, not "return
//     whatever the shorter overlap computes to" — so this is a clear
//     rejection, never a silent partial-dot-product answer.
//
// An unknown or never-upserted namespace (readDimension's found == false)
// is not an error: Query returns an empty, nil-error result, matching
// Count and Namespaces' treatment of a namespace with nothing in it yet.
//
// req.Filter, when non-empty, restricts matches to vectors whose Metadata
// contains every given key with an exactly equal value (same semantics as
// pkg/provider's own ExampleVectorStore double).
func (f *FlatVectorStore) Query(ctx context.Context, namespace string, req provider.VectorQuery) ([]provider.VectorMatch, error) {
	if req.TopK <= 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput, "localvector: Query TopK must be positive, got %d", req.TopK)
	}
	if len(req.Values) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "localvector: Query Values must be non-empty")
	}

	established, found, err := readDimension(ctx, f.store, namespace)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if len(req.Values) != established {
		return nil, cascade.Newf(cascade.KindInvalidInput, "localvector: Query vector has %d dims, namespace %q expects %d", len(req.Values), namespace, established)
	}

	it, err := f.store.Scan(ctx, dataNamespacePrefix+namespace, "")
	if err != nil {
		return nil, err
	}

	matches, scanErr := scanMatches(ctx, it, req)
	if scanErr != nil {
		return nil, scanErr
	}

	sortMatches(matches)
	if len(matches) > req.TopK {
		matches = matches[:req.TopK]
	}
	return matches, nil
}

// scanMatches drains it, decoding each (id, record) pair, scoring it
// against req.Values by cosine similarity, and keeping it only if it
// passes req.Filter. It always closes it, including on a decode error.
func scanMatches(ctx context.Context, it provider.Iterator, req provider.VectorQuery) ([]provider.VectorMatch, error) {
	var matches []provider.VectorMatch
	for it.Next(ctx) {
		values, meta, err := decodeRecord(it.Value())
		if err != nil {
			_ = it.Close()
			return nil, err
		}
		if !matchesFilter(meta, req.Filter) {
			continue
		}
		matches = append(matches, provider.VectorMatch{
			ID:       it.Key(),
			Score:    cosineSimilarity(values, req.Values),
			Metadata: meta,
		})
	}
	if err := it.Err(); err != nil {
		_ = it.Close()
		return nil, err
	}
	if err := it.Close(); err != nil {
		return nil, err
	}
	return matches, nil
}

// cosineSimilarity returns the cosine of the angle between a and b:
// dot(a,b) / (|a| * |b|). Either vector having zero magnitude leaves the
// angle undefined; FlatVectorStore scores that case 0 rather than
// propagating a 0/0 NaN or +Inf into the ranking.
func cosineSimilarity(a, b []float32) float32 {
	var dot, normA, normB float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// matchesFilter reports whether metadata contains every key/value pair in
// filter (exact equality). A nil or empty filter matches everything.
func matchesFilter(metadata, filter map[string]any) bool {
	for k, want := range filter {
		got, ok := metadata[k]
		if !ok || got != want {
			return false
		}
	}
	return true
}

// sortMatches ranks matches by descending score, breaking ties by
// ascending ID — the documented, deterministic tie-break (see
// FlatVectorStore's doc comment) that keeps ranking stable under
// `go test -shuffle=on` regardless of the Store.Scan iteration order that
// produced matches.
func sortMatches(matches []provider.VectorMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].ID < matches[j].ID
	})
}
