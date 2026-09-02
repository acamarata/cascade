// Package cache implements provider.Cache: an LRU-eviction, TTL-aware
// ephemeral key/value store layered on a provider.Store persistence
// backend (T1's Store family — never a raw SQLite dependency; local Cache
// stays under internal/storage/ per R-14.6).
//
// Persistence model: the Store IS the source of truth for a value once
// Set. The in-memory LRU index (lru.go) tracks only recency and size, for
// deciding which entries to evict under capacity pressure — it never
// caches the value itself. Consequently Get always resolves through Store
// and, on a hit, re-inserts the key into the LRU index if the index didn't
// already have it (a second Cache instance sharing the same Store, or a
// process restart, both self-heal on first access): the acceptance
// criterion "persistence round-trip: value survives cache-miss →
// Store-reload" holds because there is no separate in-memory value cache
// to go stale or empty independently of Store.
//
// TTL and eviction: Set persists an envelope (envelope.go) carrying an
// absolute expiry instant computed from the injected Clock, so Get can
// detect expiry without a second clock read anywhere else. Capacity
// pressure (item count and/or byte ceiling, Config) evicts the
// least-recently-used tracked entries — deleting them from Store too —
// until the instance is back under both limits.
//
// Purpose: concrete internal/storage/cache implementation of
//
//	pkg/provider.Cache.
//
// Inputs: a provider.Store, a runtime.Clock, and a Config (New).
// Outputs: values via Get, or a *cascade.Error for genuine Store failures
//
//	(never for an ordinary miss — see pkg/provider/cache.go's contract).
//
// Constraints: internal/storage/cache may import internal/ freely; no bare
//
//	time.Now (Clock injection only); one mutex serializes every operation,
//	including the wrapped Store calls, matching storetest.MemStore's own
//	documented trade-off (correctness over intra-instance parallelism).
//
// SPORT: internal.storage.cache.Cache/ADDED (P1-E02-W1-S02-T4).
package cache

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Config bounds a Cache instance's capacity. Zero in either field means
// "no limit on that dimension" — Set never evicts for a zero-valued
// ceiling.
type Config struct {
	// MaxItems is the maximum number of tracked entries before Set starts
	// evicting the least-recently-used ones. Zero means unlimited.
	MaxItems int
	// MaxBytes is the maximum total value-byte ceiling before Set starts
	// evicting the least-recently-used entries. Zero means unlimited.
	MaxBytes int64
}

// Stats holds the hit/miss/eviction counters Cache accumulates over its
// lifetime.
type Stats struct {
	Hits      int64
	Misses    int64
	Evictions int64
}

// Cache is the Store-backed, LRU-eviction provider.Cache driver. The zero
// value is not usable; construct with New.
type Cache struct {
	mu    sync.Mutex
	store provider.Store
	clock runtime.Clock
	cfg   Config
	index *lruIndex
	stats Stats
}

// New returns a ready-to-use Cache persisting through store, resolving TTL
// against clock, and bounded by cfg. Pass runtime.NewSystemClock() in
// production and a testkit.FrozenClock (or any structurally identical
// Clock) in tests — Cache never reads the wall clock itself.
func New(store provider.Store, clock runtime.Clock, cfg Config) *Cache {
	return &Cache{
		store: store,
		clock: clock,
		cfg:   cfg,
		index: newLRUIndex(),
	}
}

// Get implements provider.Cache.
func (c *Cache) Get(ctx context.Context, namespace, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	raw, err := c.store.Get(ctx, namespace, key)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			c.index.remove(lruKey{namespace, key})
			c.stats.Misses++
			return nil, false, nil
		}
		return nil, false, cascade.Wrapf(cascade.KindUnavailable, err, "cache.Get: namespace %q key %q", namespace, key)
	}

	expiresAt, value, decErr := decodeEnvelope(raw)
	if decErr != nil {
		return nil, false, decErr
	}
	if !expiresAt.IsZero() && !c.clock.Now().Before(expiresAt) {
		c.deleteLocked(ctx, namespace, key)
		c.stats.Misses++
		return nil, false, nil
	}

	c.index.touch(lruKey{namespace, key}, int64(len(value)))
	c.stats.Hits++
	return value, true, nil
}

// Set implements provider.Cache.
func (c *Cache) Set(ctx context.Context, namespace, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = c.clock.Now().Add(ttl)
	}
	if err := c.store.Put(ctx, namespace, key, encodeEnvelope(expiresAt, value)); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "cache.Set: namespace %q key %q", namespace, key)
	}
	c.index.touch(lruKey{namespace, key}, int64(len(value)))
	c.evictOverCapacityLocked(ctx)
	return nil
}

// Evict implements provider.Cache.
func (c *Cache) Evict(ctx context.Context, namespace, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteLocked(ctx, namespace, key)
	return nil
}

// Flush implements provider.Cache.
func (c *Cache) Flush(ctx context.Context, namespace string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	it, err := c.store.Scan(ctx, namespace, "")
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "cache.Flush: namespace %q", namespace)
	}
	var keys []string
	for it.Next(ctx) {
		keys = append(keys, it.Key())
	}
	scanErr := it.Err()
	closeErr := it.Close()
	if scanErr != nil {
		return cascade.Wrapf(cascade.KindUnavailable, scanErr, "cache.Flush: scanning namespace %q", namespace)
	}
	if closeErr != nil {
		return cascade.Wrapf(cascade.KindUnavailable, closeErr, "cache.Flush: closing scan of namespace %q", namespace)
	}
	for _, key := range keys {
		c.deleteLocked(ctx, namespace, key)
	}
	return nil
}

// Stats returns a snapshot of the accumulated hit/miss/eviction counters.
// Stats is not part of the provider.Cache interface (the ticket's task
// text calls it out separately); it is an extra, driver-specific accessor.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// deleteLocked removes namespace/key from both Store and the LRU index.
// Caller MUST hold c.mu. Store errors are swallowed here (mirroring
// provider.Cache.Evict's documented idempotent-nil-error contract) since
// every deleteLocked call site is itself already tolerant of an absent
// key; a genuine Store outage will already have surfaced through the Get
// or Set call that triggered this deletion.
func (c *Cache) deleteLocked(ctx context.Context, namespace, key string) {
	_ = c.store.Delete(ctx, namespace, key)
	c.index.remove(lruKey{namespace, key})
}

// evictOverCapacityLocked pops least-recently-used entries (Store delete +
// index removal) until both cfg.MaxItems and cfg.MaxBytes are satisfied.
// Caller MUST hold c.mu.
func (c *Cache) evictOverCapacityLocked(ctx context.Context) {
	for c.overCapacity() {
		oldest, ok := c.index.oldest()
		if !ok {
			return
		}
		c.deleteLocked(ctx, oldest.namespace, oldest.key)
		c.stats.Evictions++
	}
}

// overCapacity reports whether the index currently exceeds either
// configured ceiling. Caller MUST hold c.mu.
func (c *Cache) overCapacity() bool {
	if c.cfg.MaxItems > 0 && c.index.len() > c.cfg.MaxItems {
		return true
	}
	if c.cfg.MaxBytes > 0 && c.index.bytes > c.cfg.MaxBytes {
		return true
	}
	return false
}
