// Purpose: the provider.Queue conformance run for the Redis driver against
//   miniredis (see redis_test.go's doc comment), including the ack-timeout
//   error path driven deterministically via storetest.WithQueueClock and
//   testClock's Advance (R-14.136: no sleep-based flake). The REAL,
//   docker-provisioned server run is integration_test.go's
//   TestRedisQueueStoretestUnderDocker (Art.2).
// SPORT: providers.redis.Queue/ADDED (P1-E17-W4-S38-T6).

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/redis"
)

// testClock is a minimal, mutable Clock satisfying both redis.Clock (Now)
// and storetest.AdvanceableClock (Advance) — this package's own seam
// rather than importing internal/testkit.FrozenClock, matching
// providers/redis's Art.10.2 boundary discipline even in its own test
// files.
type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Advance(d time.Duration) time.Time {
	c.now = c.now.Add(d)
	return c.now
}

func newTestQueue(t *testing.T) (provider.Queue, *testClock) {
	t.Helper()
	conn, err := redis.Open(context.Background(), startMiniredis(t))
	if err != nil {
		t.Fatalf("redis.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	return redis.NewQueue(conn, clock, redis.Config{}), clock
}

func TestQueue_Conformance(t *testing.T) {
	var clock *testClock
	storetest.RunQueueTests(t, func(t *testing.T) provider.Queue {
		var q provider.Queue
		q, clock = newTestQueue(t)
		return q
	}, storetest.WithQueueClock(clockAdapter{&clock}))
}

// clockAdapter defers to whichever *testClock the factory most recently
// produced, since RunQueueTests calls the factory once per sub-test but
// WithQueueClock's option is bound once, before any factory call.
type clockAdapter struct {
	ref **testClock
}

func (a clockAdapter) Advance(d time.Duration) time.Time {
	return (*a.ref).Advance(d)
}

func TestQueue_BoundedCapacity(t *testing.T) {
	conn, err := redis.Open(context.Background(), startMiniredis(t))
	if err != nil {
		t.Fatalf("redis.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := redis.NewQueue(conn, &testClock{now: time.Unix(0, 0)}, redis.Config{Capacity: 1})

	ctx := context.Background()
	if _, err := q.Enqueue(ctx, "ns", []byte("a")); err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	_, err = q.Enqueue(ctx, "ns", []byte("b"))
	if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		t.Fatalf("Enqueue past capacity = %v, want KindQuotaExhausted", err)
	}
}

// TestQueue_ErrorPaths drives every method's error-wrapping branch
// against a real server told (via SetError) to fail every command — see
// cache_test.go's TestCache_ErrorPaths for why this is a real RESP error
// reply, not a simulated Go error value.
func TestQueue_ErrorPaths(t *testing.T) {
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
	clock := &testClock{now: time.Unix(0, 0)}
	ctx := context.Background()

	boundedQ := redis.NewQueue(conn, clock, redis.Config{Capacity: 1})
	srv.SetError("ERR simulated failure")
	if _, err := boundedQ.Enqueue(ctx, "ns", []byte("a")); err == nil {
		t.Fatal("Enqueue (bounded, unackedCount fails) returned nil error")
	}

	q := redis.NewQueue(conn, clock, redis.Config{})
	if _, err := q.Enqueue(ctx, "ns", []byte("a")); err == nil {
		t.Fatal("Enqueue with a failing backend returned nil error")
	}
	if _, err := q.Dequeue(ctx, "ns", time.Minute); err == nil {
		t.Fatal("Dequeue with a failing backend returned nil error")
	}
	if err := q.Ack(ctx, "ns", "bad-receipt"); err == nil {
		t.Fatal("Ack with a failing backend returned nil error")
	}
	if err := q.Nack(ctx, "ns", "bad-receipt"); err == nil {
		t.Fatal("Nack with a failing backend returned nil error")
	}
}
