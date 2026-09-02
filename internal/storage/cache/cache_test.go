// Purpose: Cache conformance (via storetest.RunCacheTests, run against
//   storetest.NewMemStore per the ticket) plus driver-specific edge cases:
//   TTL expiry (deterministic via testkit.FrozenClock, never a sleep),
//   eviction under item-count and byte-ceiling capacity, Stats accuracy,
//   and the persistence round-trip across two Cache instances sharing one
//   Store.
// Constraints: no sleeps as synchronization (Art.7.3/Art.11); no network.
// SPORT: internal.storage.cache.Cache/ADDED (P1-E02-W1-S02-T4).

package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/cache"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestCache_Conformance(t *testing.T) {
	storetest.RunCacheTests(t, func(t *testing.T) provider.Cache {
		t.Helper()
		clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
		return cache.New(storetest.NewMemStore(), clock, cache.Config{})
	})
}

func TestCache_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	c := cache.New(storetest.NewMemStore(), clock, cache.Config{})

	if err := c.Set(ctx, "ns", "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, hit, err := c.Get(ctx, "ns", "k"); err != nil || !hit {
		t.Fatalf("Get before expiry: hit=%v err=%v, want hit=true", hit, err)
	}

	clock.Advance(2 * time.Minute)
	_, hit, err := c.Get(ctx, "ns", "k")
	if err != nil {
		t.Fatalf("Get after expiry: %v", err)
	}
	if hit {
		t.Fatal("Get after TTL elapsed: hit = true, want false")
	}
}

func TestCache_NoTTLNeverExpires(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	c := cache.New(storetest.NewMemStore(), clock, cache.Config{})

	if err := c.Set(ctx, "ns", "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clock.Advance(365 * 24 * time.Hour)
	_, hit, err := c.Get(ctx, "ns", "k")
	if err != nil || !hit {
		t.Fatalf("Get with zero TTL after a year: hit=%v err=%v, want hit=true", hit, err)
	}
}

func TestCache_EvictionUnderItemCapacity(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	c := cache.New(storetest.NewMemStore(), clock, cache.Config{MaxItems: 2})

	mustSet(t, c, "k1", "v1")
	mustSet(t, c, "k2", "v2")
	mustSet(t, c, "k3", "v3") // evicts k1 (least recently used)

	if _, hit, _ := c.Get(ctx, "ns", "k1"); hit {
		t.Fatal("k1 should have been evicted under MaxItems=2, still a hit")
	}
	if _, hit, _ := c.Get(ctx, "ns", "k2"); !hit {
		t.Fatal("k2 should still be present")
	}
	if _, hit, _ := c.Get(ctx, "ns", "k3"); !hit {
		t.Fatal("k3 should still be present")
	}
	if got := c.Stats().Evictions; got != 1 {
		t.Fatalf("Stats().Evictions = %d, want 1", got)
	}
}

func TestCache_EvictionUnderByteCapacity(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	c := cache.New(storetest.NewMemStore(), clock, cache.Config{MaxBytes: 4})

	mustSet(t, c, "k1", "ab") // 2 bytes
	mustSet(t, c, "k2", "cd") // 2 bytes, total 4, still within ceiling
	mustSet(t, c, "k3", "ef") // pushes total to 6, evicts k1

	if _, hit, _ := c.Get(ctx, "ns", "k1"); hit {
		t.Fatal("k1 should have been evicted under MaxBytes=4, still a hit")
	}
	if _, hit, _ := c.Get(ctx, "ns", "k3"); !hit {
		t.Fatal("k3 should still be present")
	}
}

func TestCache_StatsAccuracy(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	c := cache.New(storetest.NewMemStore(), clock, cache.Config{})

	if _, _, err := c.Get(ctx, "ns", "missing"); err != nil {
		t.Fatalf("Get miss: %v", err)
	}
	mustSet(t, c, "k1", "v1")
	if _, _, err := c.Get(ctx, "ns", "k1"); err != nil {
		t.Fatalf("Get hit: %v", err)
	}

	stats := c.Stats()
	if stats.Hits != 1 {
		t.Fatalf("Stats().Hits = %d, want 1", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Fatalf("Stats().Misses = %d, want 1", stats.Misses)
	}
}

// TestCache_PersistenceRoundTrip proves the acceptance criterion directly:
// a second Cache instance, with its own empty in-memory LRU index, still
// observes a value Set through a first instance because both share one
// underlying provider.Store.
func TestCache_PersistenceRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))

	writer := cache.New(store, clock, cache.Config{})
	if err := writer.Set(ctx, "ns", "k", []byte("durable"), time.Hour); err != nil {
		t.Fatalf("Set via writer: %v", err)
	}

	reader := cache.New(store, clock, cache.Config{})
	value, hit, err := reader.Get(ctx, "ns", "k")
	if err != nil {
		t.Fatalf("Get via reader: %v", err)
	}
	if !hit {
		t.Fatal("reader (fresh LRU index) did not see writer's Set through the shared Store")
	}
	if string(value) != "durable" {
		t.Fatalf("reader value = %q, want %q", value, "durable")
	}
}

func mustSet(t *testing.T, c *cache.Cache, key, value string) {
	t.Helper()
	if err := c.Set(context.Background(), "ns", key, []byte(value), 0); err != nil {
		t.Fatalf("Set(%s): %v", key, err)
	}
}
