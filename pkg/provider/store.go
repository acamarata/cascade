// Purpose: declare the Store family contract — the key-value abstraction
//   every cascade.db domain (R-14.5: context, memory, audit, secrets,
//   sessions, config, retrieval, blobs, queue, jobs) and every plugin's
//   namespaced storage slot is built on.
// Inputs: a namespace + key/prefix per call; a transaction closure for Tx.
// Outputs: values as []byte, an Iterator for Scan, or a pkg/cascade taxonomy
//   error.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); every
//   error a driver returns MUST be a *cascade.Error with a taxonomy Kind —
//   never a raw fmt.Errorf/errors.New (boundary lint, internal/build).
// SPORT: pkg.provider.Store/ADDED (P1-E02-W1-S02-T1).

package provider

import "context"

// Store is the key-value abstraction every cascade.db domain and every
// plugin's namespaced storage slot is built on. Every method is
// namespace-scoped: the namespace argument selects the logical domain (one
// of the R-14.5 ten) or a plugin's own PluginStorage slot, so one physical
// driver instance (SQLite locally, Postgres on server profile) can safely
// back every domain and every plugin without key collisions.
//
// NOTE (design decision, contract silent on Store-level scoping): the
// contract's task text lists only "Get/Put/Delete/Scan/Tx" without saying
// whether Store is namespace-scoped like VectorStore (R-14.4 makes that
// explicit only for VectorStore). This declaration makes Store
// namespace-scoped too, for consistency with VectorStore and because
// 02-TARGET-STRUCTURE's "Storage scoping: plugin gets namespaced
// PluginStorage" and R-14.5's ten-domain cascade.db contract both require a
// scoping argument somewhere in the Store surface — adding it here, once,
// on every method is the smallest sufficient surface that satisfies both
// without a second scoping mechanism layered on top.
//
// A driver's Get returns a *cascade.Error with cascade.KindNotFound when
// the key does not exist in the namespace. Put and Delete report
// cascade.KindInvalidInput for a malformed namespace/key and
// cascade.KindUnavailable when the underlying store cannot be reached.
type Store interface {
	// Get returns the value stored under key in namespace, or a
	// cascade.KindNotFound error if no such key exists.
	Get(ctx context.Context, namespace, key string) ([]byte, error)

	// Put writes value under key in namespace, creating or overwriting the
	// existing entry unconditionally. Use Tx with Tx.CompareAndSwap for
	// conditional writes.
	Put(ctx context.Context, namespace, key string, value []byte) error

	// Delete removes key from namespace. Deleting an already-absent key is
	// not an error — Delete is idempotent.
	Delete(ctx context.Context, namespace, key string) error

	// Scan returns an Iterator over every key in namespace with the given
	// prefix (an empty prefix iterates the whole namespace). The caller
	// MUST call Iterator.Close when done, including on early exit.
	Scan(ctx context.Context, namespace, prefix string) (Iterator, error)

	// Tx runs fn inside one atomic transaction against the store. If fn
	// returns a non-nil error, every write fn made through tx is rolled
	// back and Tx returns that same error. A conditional-update conflict
	// detected by Tx.CompareAndSwap surfaces as a cascade.KindConflict
	// error from fn (and therefore from Tx).
	Tx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is the transactional view of a Store passed into Store.Tx's closure.
// Every method behaves as it does on Store, scoped to the enclosing
// transaction, plus CompareAndSwap for optimistic-concurrency writes.
type Tx interface {
	// Get returns the value stored under key in namespace as of this
	// transaction's isolation snapshot, or a cascade.KindNotFound error.
	Get(ctx context.Context, namespace, key string) ([]byte, error)

	// Put writes value under key in namespace within the transaction.
	Put(ctx context.Context, namespace, key string, value []byte) error

	// Delete removes key from namespace within the transaction.
	Delete(ctx context.Context, namespace, key string) error

	// CompareAndSwap writes new under key in namespace only if the key's
	// current value equals old exactly (byte-for-byte). A nil old means
	// "the key must not currently exist" (a conditional create). On a
	// mismatch — the stored value differs from old, or old was nil but the
	// key already exists — CompareAndSwap returns a cascade.KindConflict
	// error and makes no write.
	CompareAndSwap(ctx context.Context, namespace, key string, old, newValue []byte) error
}

// Iterator walks the results of a Store.Scan call in key order. Callers
// drive it with the standard "for it.Next(ctx) { ... }; if err :=
// it.Err(); err != nil { ... }" loop and MUST call Close exactly once when
// finished, including when the loop exits early.
type Iterator interface {
	// Next advances the iterator and reports whether a value is available.
	// Next returns false at end-of-results or on error; callers check Err
	// after a false return to distinguish the two.
	Next(ctx context.Context) bool

	// Key returns the current entry's key. Valid only after a Next call
	// that returned true.
	Key() string

	// Value returns the current entry's value. Valid only after a Next
	// call that returned true.
	Value() []byte

	// Err returns the first error encountered during iteration, or nil if
	// iteration completed (or is still in progress) without one.
	Err() error

	// Close releases resources held by the iterator. Close is idempotent
	// and safe to call after a partial iteration.
	Close() error
}
