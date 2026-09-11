// Purpose: Cache, the provider.Cache driver over a live Conn — the server
//   profile's Cache leg (P1-E17-W4-S38-T6), the same family interface the
//   local internal/storage/cache.Cache implements, over the same
//   storetest.RunCacheTests suite.
// Inputs: a *Conn (New) plus namespace/key/value/ttl per call.
// Outputs: values via Get, or a *cascade.Error carrying a taxonomy Kind —
//   never for an ordinary miss (pkg/provider/cache.go's own contract).
// Constraints: providers/** imports pkg/** only (Art.10.2); Redis's own
//   native per-key TTL (SET ... EX) implements the family's TTL semantics
//   directly — no separate expiry bookkeeping is needed, unlike the
//   Store-backed local driver, because the backend itself already expires
//   entries; Flush uses SCAN (never KEYS, which blocks the server) to
//   enumerate a namespace's keys before deleting them.
// SPORT: providers.redis.Cache/ADDED (P1-E17-W4-S38-T6).

package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/acamarata/cascade/pkg/cascade"
)

// cacheKeyPrefix namespaces every key this driver writes so a Cache and a
// Queue sharing the same underlying Redis database (and therefore the same
// keyspace) never collide, even when both are opened against the same
// *Conn's namespace.
const cacheKeyPrefix = "cascade:cache:"

// Cache is the Redis-backed provider.Cache driver. The zero value is not
// usable; construct with NewCache.
type Cache struct {
	conn *Conn
}

// NewCache returns a Cache issuing every command over conn.
func NewCache(conn *Conn) *Cache {
	return &Cache{conn: conn}
}

func cacheKey(namespace, key string) string {
	return cacheKeyPrefix + namespace + ":" + key
}

// cacheScanPattern returns the SCAN MATCH pattern covering every key Flush
// must delete for namespace.
func cacheScanPattern(namespace string) string {
	return cacheKeyPrefix + namespace + ":*"
}

// Get implements provider.Cache.
func (c *Cache) Get(ctx context.Context, namespace, key string) ([]byte, bool, error) {
	val, err := c.conn.rdb.Get(ctx, cacheKey(namespace, key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, false, nil
		}
		return nil, false, cascade.Wrapf(classifyErr(err), err, "redis.Cache.Get: namespace %q key %q", namespace, key)
	}
	return val, true, nil
}

// Set implements provider.Cache. ttl of zero maps to Redis's own "no
// expiration" SET option, matching the family contract's "no explicit
// expiry" meaning (pkg/provider/cache.go).
func (c *Cache) Set(ctx context.Context, namespace, key string, value []byte, ttl time.Duration) error {
	if err := c.conn.rdb.Set(ctx, cacheKey(namespace, key), value, ttl).Err(); err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Cache.Set: namespace %q key %q", namespace, key)
	}
	return nil
}

// Evict implements provider.Cache. Deleting an absent key is not an error —
// Redis's DEL already reports zero-deleted rather than erroring, so no
// special-casing is needed to satisfy the family's idempotent-evict
// contract.
func (c *Cache) Evict(ctx context.Context, namespace, key string) error {
	if err := c.conn.rdb.Del(ctx, cacheKey(namespace, key)).Err(); err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Cache.Evict: namespace %q key %q", namespace, key)
	}
	return nil
}

// Flush implements provider.Cache: it enumerates every key under
// namespace via SCAN (cursor-based, never blocking the server the way KEYS
// would) and deletes them in one batch.
func (c *Cache) Flush(ctx context.Context, namespace string) error {
	keys, err := c.scanKeys(ctx, namespace)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	if err := c.conn.rdb.Del(ctx, keys...).Err(); err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Cache.Flush: deleting namespace %q", namespace)
	}
	return nil
}

// scanKeys collects every key matching namespace's Flush pattern via a
// cursor-based SCAN loop.
func (c *Cache) scanKeys(ctx context.Context, namespace string) ([]string, error) {
	var (
		keys   []string
		cursor uint64
	)
	pattern := cacheScanPattern(namespace)
	for {
		batch, next, err := c.conn.rdb.Scan(ctx, cursor, pattern, 0).Result()
		if err != nil {
			return nil, cascade.Wrapf(classifyErr(err), err, "redis.Cache.Flush: scanning namespace %q", namespace)
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			return keys, nil
		}
	}
}
