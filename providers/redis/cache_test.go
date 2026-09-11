// Purpose: the provider.Cache conformance run for the Redis driver against
//   miniredis (see redis_test.go's doc comment for why: a real-RESP-
//   protocol in-memory server, not a hand-rolled fake, giving this the
//   same untagged/no-docker coverage lane providers/postgres reserves for
//   its pure-function unit tests). The REAL, docker-provisioned server run
//   is integration_test.go's TestRedisCacheStoretestUnderDocker (Art.2).
// SPORT: providers.redis.Cache/ADDED (P1-E17-W4-S38-T6).

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/redis"
)

func newTestCache(t *testing.T) provider.Cache {
	t.Helper()
	conn, err := redis.Open(context.Background(), startMiniredis(t))
	if err != nil {
		t.Fatalf("redis.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return redis.NewCache(conn)
}

func TestCache_Conformance(t *testing.T) {
	storetest.RunCacheTests(t, newTestCache)
}

// TestCache_TTLExpires proves Set's ttl argument is honored by the real
// backend: miniredis implements Redis's own TTL/expiry semantics, and
// FastForward advances its internal clock deterministically (no sleep),
// which storetest.RunCacheTests does not itself assert (the family
// contract leaves eviction timing to the driver).
func TestCache_TTLExpires(t *testing.T) {
	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(srv.Close)
	conn, err := redis.Open(context.Background(), "redis://"+srv.Addr())
	if err != nil {
		t.Fatalf("redis.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := redis.NewCache(conn)

	ctx := context.Background()
	if err := c.Set(ctx, "ns", "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	srv.FastForward(2 * time.Minute)
	_, hit, err := c.Get(ctx, "ns", "k")
	if err != nil {
		t.Fatalf("Get after TTL elapsed: %v", err)
	}
	if hit {
		t.Fatal("Get hit = true after TTL elapsed, want false")
	}
}

// TestCache_ErrorPaths proves every method's error-wrapping branch by
// driving a real server that has been told (via SetError, miniredis's own
// fault-injection hook) to fail every command — a genuine RESP error
// reply, not a simulated Go error value.
func TestCache_ErrorPaths(t *testing.T) {
	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(srv.Close)
	conn, err := redis.Open(context.Background(), "redis://"+srv.Addr())
	if err != nil {
		t.Fatalf("redis.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := redis.NewCache(conn)
	ctx := context.Background()

	srv.SetError("ERR simulated failure")
	if _, _, err := c.Get(ctx, "ns", "k"); err == nil {
		t.Fatal("Get with a failing backend returned nil error")
	}
	if err := c.Set(ctx, "ns", "k", []byte("v"), 0); err == nil {
		t.Fatal("Set with a failing backend returned nil error")
	}
	if err := c.Evict(ctx, "ns", "k"); err == nil {
		t.Fatal("Evict with a failing backend returned nil error")
	}
	if err := c.Flush(ctx, "ns"); err == nil {
		t.Fatal("Flush with a failing backend returned nil error")
	}
}
