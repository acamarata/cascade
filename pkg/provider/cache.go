// Purpose: declare the Cache family contract — an LRU-compatible,
//   TTL-aware ephemeral store, distinct from Store in that entries may be
//   evicted by the driver at any time (capacity pressure, expiry) without
//   that eviction being an error condition for the caller.
// Inputs: a namespace + key, a value + TTL (Set).
// Outputs: a value plus a hit/miss flag (Get), or a pkg/cascade taxonomy
//   error for genuine failures (not for ordinary misses).
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); Get
//   reports misses via a bool, not an error — Cache misses (including
//   driver-initiated eviction) are expected traffic, not a taxonomy error
//   path, matching the LRU-compatible contract the ticket text specifies.
// SPORT: pkg.provider.Cache/ADDED (P1-E02-W1-S02-T1).

package provider

import (
	"context"
	"time"
)

// Cache is an ephemeral, LRU-compatible key/value store scoped per
// namespace. A driver MAY evict any entry before its TTL elapses under
// capacity pressure; callers must always be prepared for a Get miss on a
// key they previously Set.
//
// NOTE (design decision, contract silent on Get's error shape): Get returns
// (value, hit bool, error) rather than treating a miss as a
// cascade.KindNotFound error. This mirrors ordinary Go cache idiom (a miss
// is expected, high-frequency, non-exceptional) and keeps the one
// genuinely-exceptional error path (driver failure) separate from routine
// cache traffic — the storetest suite's "key-not-found" error-path case
// exercises Store.Get and BlobStore.Get, both of which are true lookups by
// identity rather than a cache's best-effort retrieval.
type Cache interface {
	// Get returns the value stored under key in namespace and hit=true, or
	// hit=false if the key is absent or was evicted. A non-nil error
	// indicates a driver failure, not a miss; on error, hit is always
	// false and value is always nil.
	Get(ctx context.Context, namespace, key string) (value []byte, hit bool, err error)

	// Set writes value under key in namespace with the given
	// time-to-live. A ttl of zero means "no explicit expiry" — the entry
	// is still subject to LRU eviction under capacity pressure, just not
	// to time-based expiry.
	Set(ctx context.Context, namespace, key string, value []byte, ttl time.Duration) error

	// Evict removes key from namespace immediately, ahead of its TTL.
	// Evicting an absent key is not an error.
	Evict(ctx context.Context, namespace, key string) error

	// Flush removes every entry in namespace.
	Flush(ctx context.Context, namespace string) error
}
