// Package localvector is the local profile's default provider.VectorStore
// driver: a flat, brute-force linear cosine-similarity scan over an
// injected provider.Store. Brute force is a deliberate design choice, not
// a placeholder — it is EXACT (no approximation error an approximate
// nearest-neighbor index would introduce) and the local profile's
// personal-corpus scale (docs, notes, session history — not a web-scale
// index) makes an O(n) scan per query cheap in absolute terms. An
// approximate-index driver and an embedded-native-index driver are both
// explicit post-P1 provider slots (04-PEWS-PLAN-W1-W3.md §Epic-B S-03.T4)
// and must never appear as an import or a real code path anywhere in this
// package's non-test files.
//
// Purpose: FlatVectorStore — the R-14.4 canonical provider.VectorStore
//
//	surface (Upsert, Query, Delete, Count, Namespaces), persisted through a
//	caller-injected provider.Store rather than any in-memory-only
//	shortcut.
//
// Inputs: a provider.Store (New's only argument) plus, per call, a
//
//	namespace string and the operation's vectors/query/ids.
//
// Outputs: matches ranked by descending cosine similarity, or a
//
//	pkg/cascade taxonomy error.
//
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(Art.10.2) — this file imports only pkg/cascade, pkg/provider, and
//	stdlib. No CGO, no third-party vector library, no new dependency
//	(R-14.115 — go.mod is owned by a concurrently dispatched ticket).
//	Pure Go math (encoding/binary, math.Sqrt) is sufficient for
//	brute-force cosine search at this scale.
//
// SPORT: providers.localvector.FlatVectorStore/ADDED,
//
//	providers.localvector.New/ADDED (P1-E02-W1-S03-T4).
package localvector

import (
	"context"
	"encoding/binary"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// dataNamespacePrefix scopes the injected Store's own namespace argument
// for per-vector records, keyed by (namespace, id). Prefixing keeps vector
// data disjoint from indexNamespace below within the same underlying
// Store, so a caller's chosen namespace string can never collide with
// this driver's internal bookkeeping namespace.
const dataNamespacePrefix = "localvector/vectors/"

// indexNamespace holds one record per namespace this driver has ever
// established a dimensionality for, keyed by the namespace name itself,
// valued by its 4-byte little-endian dimension. This is what makes
// Namespaces (and the "established dimensionality" half of
// provider.VectorStore.Namespaces's contract) survive a process restart:
// without a persisted index, an emptied namespace (every vector deleted)
// would silently vanish from Namespaces on the next run, even though the
// interface promises a namespace with an established dimensionality stays
// listed.
const indexNamespace = "localvector/index"

// FlatVectorStore is the local profile's provider.VectorStore: every
// Query is an O(n) linear scan over the target namespace's vectors,
// computing cosine similarity against each and returning the top-k by
// descending score. The zero value is not usable; construct with New.
//
// Metric: cosine similarity only (no raw dot-product or Euclidean mode).
// Normalization happens at QUERY time, on the fly, against the raw values
// Upsert stored — vectors are never rewritten in place. Because cosine
// similarity is scale-invariant (cos(a,b) = dot(a,b) / (|a| * |b|)), this
// produces the identical score a write-time normalization would, up to
// floating-point rounding; query-time normalization is preferred here
// because it keeps the persisted record exactly what the caller supplied
// (useful for callers that read raw values back out via other means) and
// needs no re-normalization pass if Upsert is ever extended to support an
// alternate metric per namespace. A zero-magnitude vector (all zeros) has
// no defined direction; FlatVectorStore scores it 0 against anything
// (never NaN/Inf) rather than treating 0/0 as an error.
//
// Ties: when two matches have equal score, ranking breaks the tie by
// ascending ID (byte-wise string comparison) — deterministic under
// `-shuffle=on` and independent of Go's unspecified map/slice iteration
// order (see recordScan, which drains the Store's Iterator into a slice
// before FlatVectorStore ever sorts it).
type FlatVectorStore struct {
	store provider.Store
}

// New returns a FlatVectorStore persisting through store. store is
// typically an in-memory reference (storetest.NewMemStore, this ticket's
// own test substrate) in tests and the providers/sqlite Store in
// production wiring (that wiring is a different ticket) — FlatVectorStore
// itself never opens or knows about a concrete backend.
func New(store provider.Store) *FlatVectorStore {
	return &FlatVectorStore{store: store}
}

var _ provider.VectorStore = (*FlatVectorStore)(nil)

// getter is satisfied by both provider.Store and provider.Tx (both
// declare an identical Get method), so readDimension can run either as a
// standalone read (Query/Count/Namespaces) or as one step of an atomic
// Upsert transaction, without duplicating the decode logic.
type getter interface {
	Get(ctx context.Context, namespace, key string) ([]byte, error)
}

// readDimension returns the dimensionality this driver has established
// for ns, or found=false if ns has never had a vector upserted into it.
func readDimension(ctx context.Context, g getter, ns string) (dim int, found bool, err error) {
	val, err := g.Get(ctx, indexNamespace, ns)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if len(val) != 4 {
		return 0, false, cascade.Newf(cascade.KindIntegrity, "localvector: corrupt dimension index record for namespace %q (want 4 bytes, got %d)", ns, len(val))
	}
	return int(binary.LittleEndian.Uint32(val)), true, nil
}

// encodeDim little-endian-encodes a positive dimension count for
// indexNamespace.
func encodeDim(dim int) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(dim)) //nolint:gosec // dim is a validated small vector length
	return buf
}

// Upsert implements provider.VectorStore. Every vector in one call must
// share the same length, and that length must match namespace's already
// established dimensionality (if any) — a mismatch, an empty ID, or a
// nil/empty Values reports cascade.KindInvalidInput and writes nothing
// (validated before the transaction opens). The write itself — the
// namespace's dimension index entry (if this call is the first ever for
// namespace) plus every vector record — commits atomically via
// provider.Store.Tx, so a concurrent reader never observes a namespace
// whose dimension index is set but whose first vector isn't there yet, or
// vice versa.
func (f *FlatVectorStore) Upsert(ctx context.Context, namespace string, vectors []provider.Vector) error {
	if len(vectors) == 0 {
		return nil
	}
	dim := len(vectors[0].Values)
	if dim == 0 {
		return cascade.New(cascade.KindInvalidInput, "localvector: Upsert vector Values must be non-empty")
	}
	for _, v := range vectors {
		if v.ID == "" {
			return cascade.New(cascade.KindInvalidInput, "localvector: Upsert vector ID must be non-empty")
		}
		if len(v.Values) == 0 {
			return cascade.Newf(cascade.KindInvalidInput, "localvector: Upsert vector %q Values must be non-empty", v.ID)
		}
		if len(v.Values) != dim {
			return cascade.Newf(cascade.KindInvalidInput, "localvector: Upsert vector %q has %d dims, batch established %d", v.ID, len(v.Values), dim)
		}
	}

	return f.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		established, found, err := readDimension(ctx, tx, namespace)
		if err != nil {
			return err
		}
		if found && established != dim {
			return cascade.Newf(cascade.KindInvalidInput, "localvector: namespace %q established dimensionality %d, Upsert supplied %d", namespace, established, dim)
		}
		if !found {
			if err := tx.Put(ctx, indexNamespace, namespace, encodeDim(dim)); err != nil {
				return err
			}
		}
		dataNS := dataNamespacePrefix + namespace
		for _, v := range vectors {
			rec, err := encodeRecord(v.Values, v.Metadata)
			if err != nil {
				return cascade.Wrapf(cascade.KindInternal, err, "localvector: encode vector %q", v.ID)
			}
			if err := tx.Put(ctx, dataNS, v.ID, rec); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete implements provider.VectorStore. Deleting an absent ID is not an
// error (matches provider.Store.Delete's own idempotence). Delete never
// removes namespace's dimension index entry — an emptied namespace keeps
// its established dimensionality, per provider.VectorStore.Namespaces's
// "currently has vectors OR an established dimensionality for" contract.
func (f *FlatVectorStore) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	dataNS := dataNamespacePrefix + namespace
	return f.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		for _, id := range ids {
			if err := tx.Delete(ctx, dataNS, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Count implements provider.VectorStore: the number of vectors currently
// stored in namespace (0 for an unknown or emptied namespace, never an
// error).
func (f *FlatVectorStore) Count(ctx context.Context, namespace string) (int, error) {
	it, err := f.store.Scan(ctx, dataNamespacePrefix+namespace, "")
	if err != nil {
		return 0, err
	}
	n := 0
	scanErr := drainCount(ctx, it, func([]byte) error { n++; return nil })
	if scanErr != nil {
		return 0, scanErr
	}
	return n, nil
}

// Namespaces implements provider.VectorStore: every namespace this
// driver has ever established a dimensionality for, sorted ascending for
// deterministic output (Art.11 — no `-shuffle=on` flake from map/iterator
// order).
func (f *FlatVectorStore) Namespaces(ctx context.Context) ([]string, error) {
	it, err := f.store.Scan(ctx, indexNamespace, "")
	if err != nil {
		return nil, err
	}
	var out []string
	scanErr := drainKeys(ctx, it, func(key string) error { out = append(out, key); return nil })
	if scanErr != nil {
		return nil, scanErr
	}
	sort.Strings(out)
	return out, nil
}
