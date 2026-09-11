//go:build integration

// Purpose: the Redis storetest-under-docker lane's real conformance run —
//
//	RunCacheTests and RunQueueTests against a REAL Redis server, never a
//	self-authored RESP dialect or hand-rolled in-memory fake (Art.2). The
//	miniredis-backed runs in cache_test.go/queue_test.go give the
//	untagged, no-docker coverage lane; this file is the docker-tagged
//	proof the ticket's own checks name.
//
// Inputs: CASCADE_TEST_REDIS_URL, a real reachable Redis server's URL.
//
// Constraints: go:build integration only — no "postgres" or "redis" tag,
//
//	matching the ticket's own check commands
//	(`go test ./providers/redis/ -tags=integration -run ...`) and
//	providers/pgvector/integration_test.go's tag-pair precedent, minus
//	the "postgres" half since this package carries no such gate.
//
// SPORT: providers.redis/ADDED (P1-E17-W4-S38-T6).
package redis_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/redis"
)

func realRedisURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("CASCADE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("CASCADE_TEST_REDIS_URL not set — this lane requires a real reachable Redis server")
	}
	return url
}

// TestRedisCacheStoretestUnderDocker is the ticket's named CI entry point
// for the Cache family against a REAL server.
func TestRedisCacheStoretestUnderDocker(t *testing.T) {
	url := realRedisURL(t)
	storetest.RunCacheTests(t, func(t *testing.T) provider.Cache {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := redis.Open(ctx, url)
		if err != nil {
			t.Fatalf("redis.Open: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return redis.NewCache(conn)
	})
}

// TestRedisQueueStoretestUnderDocker is the ticket's named CI entry point
// for the Queue family against a REAL server. No WithQueueClock: the
// suite's real-time polling fallback proves the ack-timeout path against
// the server's own wall-clock-driven TTL-adjacent behavior, complementing
// the deterministic miniredis-backed run in queue_test.go.
func TestRedisQueueStoretestUnderDocker(t *testing.T) {
	url := realRedisURL(t)
	storetest.RunQueueTests(t, func(t *testing.T) provider.Queue {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := redis.Open(ctx, url)
		if err != nil {
			t.Fatalf("redis.Open: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return redis.NewQueue(conn, systemClock{}, redis.Config{})
	})
}

// systemClock is the real-time redis.Clock this lane uses (no clock seam
// needed against a real server whose own TTL-adjacent timing is already
// real).
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// TestOpen_UnreachableServer_Real proves the unreachable-server error path
// against a real (non-listening) address, distinct from redis_test.go's
// miniredis-backed version, on this lane.
func TestOpen_UnreachableServer_Real(t *testing.T) {
	realRedisURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := redis.Open(ctx, "redis://127.0.0.1:1/0")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable", err)
	}
}
