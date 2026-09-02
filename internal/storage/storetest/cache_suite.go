// Purpose: RunCacheTests — the provider.Cache conformance suite.
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// RunCacheTests exercises every provider.Cache method against a driver
// produced by newCache.
func RunCacheTests(t *testing.T, newCache CacheFactory) {
	t.Helper()
	t.Run("SetGetHit", func(t *testing.T) { testCacheSetGetHit(t, newCache(t)) })
	t.Run("GetMiss", func(t *testing.T) { testCacheGetMiss(t, newCache(t)) })
	t.Run("Evict", func(t *testing.T) { testCacheEvict(t, newCache(t)) })
	t.Run("Flush", func(t *testing.T) { testCacheFlush(t, newCache(t)) })
	t.Run("NamespaceIsolation", func(t *testing.T) { testCacheNamespaceIsolation(t, newCache(t)) })
}

func testCacheSetGetHit(t *testing.T, c provider.Cache) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, c.Set(ctx, "ns", "k", []byte("v"), time.Minute), "Set")
	got, hit, err := c.Get(ctx, "ns", "k")
	requireNoError(t, err, "Get")
	if !hit {
		t.Fatal("Get hit = false immediately after Set, want true")
	}
	requireBytesEqual(t, got, []byte("v"), "Get value")
}

func testCacheGetMiss(t *testing.T, c provider.Cache) {
	t.Helper()
	ctx := testContext(t)
	got, hit, err := c.Get(ctx, "ns", "never-set")
	requireNoError(t, err, "Get of unset key (miss is not an error)")
	if hit {
		t.Fatal("Get hit = true for unset key, want false")
	}
	if got != nil {
		t.Fatalf("Get value on miss = %q, want nil", got)
	}
}

func testCacheEvict(t *testing.T, c provider.Cache) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, c.Set(ctx, "ns", "k", []byte("v"), 0), "Set")
	requireNoError(t, c.Evict(ctx, "ns", "k"), "Evict")
	_, hit, err := c.Get(ctx, "ns", "k")
	requireNoError(t, err, "Get after Evict")
	if hit {
		t.Fatal("Get hit = true after Evict, want false")
	}
	requireNoError(t, c.Evict(ctx, "ns", "never-set"), "Evict of absent key (want idempotent nil)")
}

func testCacheFlush(t *testing.T, c provider.Cache) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, c.Set(ctx, "ns", "k1", []byte("v1"), 0), "Set k1")
	requireNoError(t, c.Set(ctx, "ns", "k2", []byte("v2"), 0), "Set k2")
	requireNoError(t, c.Flush(ctx, "ns"), "Flush")
	for _, k := range []string{"k1", "k2"} {
		_, hit, err := c.Get(ctx, "ns", k)
		requireNoError(t, err, "Get("+k+") after Flush")
		if hit {
			t.Fatalf("Get(%s) hit = true after Flush, want false", k)
		}
	}
}

func testCacheNamespaceIsolation(t *testing.T, c provider.Cache) {
	t.Helper()
	ctx := testContext(t)
	requireNoError(t, c.Set(ctx, "a", "k", []byte("in-a"), 0), "Set ns a")
	_, hit, err := c.Get(ctx, "b", "k")
	requireNoError(t, err, "Get same key in ns b")
	if hit {
		t.Fatal("Get hit = true for key set only in a different namespace, want false")
	}
}
