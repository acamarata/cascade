// Purpose: a runnable godoc Example for provider.Cache (Art.10.6), backed
//   by a minimal in-memory double (see example_store_test.go for the
//   package-level "why a local double, not storetest" rationale).
// Constraints: this double's TTL check uses time.Now() directly rather than
//   an injected Clock — pkg/provider must not import internal/ (Art.10.2),
//   so internal/runtime.Clock is unreachable here, and forbidigo exempts
//   _test.go files from the bare-time.Now ban (.golangci.yml exclusions,
//   Art.7.3) for exactly this reason.
// SPORT: pkg.provider.Cache/ADDED (P1-E02-W1-S02-T1 CR follow-up).

package provider_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

type exampleCacheEntry struct {
	value     []byte
	expiresAt time.Time // zero means no expiry
}

// exampleCache is a real, minimal TTL-aware cache: Get checks expiresAt
// against the current time and reports a miss for an expired entry instead
// of returning stale data.
type exampleCache struct {
	mu   sync.Mutex
	data map[string]map[string]exampleCacheEntry
}

func newExampleCache() *exampleCache {
	return &exampleCache{data: make(map[string]map[string]exampleCacheEntry)}
}

func (c *exampleCache) Get(_ context.Context, namespace, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.data[namespace][key]
	if !ok {
		return nil, false, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		delete(c.data[namespace], key)
		return nil, false, nil
	}
	return entry.value, true, nil
}

func (c *exampleCache) Set(_ context.Context, namespace, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data[namespace] == nil {
		c.data[namespace] = make(map[string]exampleCacheEntry)
	}
	entry := exampleCacheEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	c.data[namespace][key] = entry
	return nil
}

func (c *exampleCache) Evict(_ context.Context, namespace, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data[namespace], key)
	return nil
}

func (c *exampleCache) Flush(_ context.Context, namespace string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, namespace)
	return nil
}

// ExampleCache demonstrates Set with a TTL, a hit, an Evict, and the
// resulting miss — a miss is a (nil, false, nil) return, never an error.
func ExampleCache() {
	ctx := context.Background()
	var cache provider.Cache = newExampleCache()

	if err := cache.Set(ctx, "sessions", "token-1", []byte("session-data"), time.Minute); err != nil {
		fmt.Println("set error:", err)
		return
	}

	value, hit, err := cache.Get(ctx, "sessions", "token-1")
	if err != nil {
		fmt.Println("get error:", err)
		return
	}
	fmt.Println(hit, string(value))

	if err := cache.Evict(ctx, "sessions", "token-1"); err != nil {
		fmt.Println("evict error:", err)
		return
	}
	_, hit, err = cache.Get(ctx, "sessions", "token-1")
	if err != nil {
		fmt.Println("get error:", err)
		return
	}
	fmt.Println(hit)

	// Output:
	// true session-data
	// false
}
