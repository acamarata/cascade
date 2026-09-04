// Purpose: happy-path Publish/Subscribe/Replay, the durable-restart proof
//
//	(TestEventBusReplayCursor, a required check) against a REAL sqlite
//	Store at t.TempDir() (Art.2 real-counterpart fixture — the ticket text
//	names "t.TempDir store" explicitly for this scenario), and the
//	required Publish error-path test (TestEventBusPublishError). Other
//	test files in this package (bus_ordering_test.go,
//	bus_backpressure_test.go, bus_concurrency_test.go, cursor_test.go,
//	envelope_test.go) are R-14.117-authorized in-package splits, kept
//	under Art.10.3's caps.
//
// Constraints: Art.7.1 — every test writes only under t.TempDir(); Art.11 —
//
//	no sleeps as synchronization anywhere in this package's tests.
//
// SPORT: internal.events.Bus/ADDED (tests) (P1-E03-W1-S04-T3).
package events_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// TestBus_PublishSubscribe_HappyPath proves the base acceptance criterion:
// typed events published via Publish are delivered to active
// subscriptions in order.
func TestBus_PublishSubscribe_HappyPath(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	sub, err := bus.Subscribe(ctx, "ns", "consumer-a", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	want := []events.EventKind{"k1", "k2", "k3"}
	for _, k := range want {
		if _, err := bus.Publish(ctx, "ns", k, "test", []byte(k)); err != nil {
			t.Fatalf("Publish(%s): %v", k, err)
		}
	}

	for i, k := range want {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatalf("Events closed early at index %d", i)
			}
			if ev.Kind != k {
				t.Fatalf("event %d Kind = %q, want %q", i, ev.Kind, k)
			}
			if ev.Seq != uint64(i+1) {
				t.Fatalf("event %d Seq = %d, want %d", i, ev.Seq, i+1)
			}
			if !ev.Timestamp.Equal(clock.Now()) {
				t.Fatalf("event %d Timestamp = %v, want %v", i, ev.Timestamp, clock.Now())
			}
		case err := <-sub.Errs:
			t.Fatalf("unexpected Errs receive: %v", err)
		}
	}
}

// TestEventBusReplayCursor is a required check
// (`go test -run TestEventBusReplayCursor`). It proves the central
// acceptance criterion: named replay cursors survive a SIMULATED RESTART —
// modeled here as closing and reopening a real sqlite Driver at the same
// t.TempDir() path, so the only thing carrying state across "processes" is
// the on-disk database, exactly as a real kill -9 would leave it — and the
// consumer resumes from exactly its last committed offset, with no event
// before the cursor re-delivered.
func TestEventBusReplayCursor(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	const namespace = "queue"
	const cursorName = "consumer-a"

	replayCursorProcessOne(ctx, t, dbPath, clock, namespace, cursorName)
	replayCursorProcessTwo(ctx, t, dbPath, clock, namespace, cursorName)
}

// replayCursorProcessOne stands in for a process that published three
// events, consumed and committed the first two, then vanished without a
// clean shutdown. It establishes that state DIRECTLY, writing the cursor
// record through the Store API, rather than by subscribing and counting
// channel receives.
//
// A live subscription cannot establish it at all. The delivery goroutine
// commits the cursor after each SEND onto the subscriber's channel, and
// that channel is buffered, so with three events and an eight-slot buffer
// the loop pushes all three and commits seq 3 long before a consumer has
// read two of them. Which value the cursor holds when the driver closes is
// then decided by a race between two goroutines, and only one of its three
// outcomes is the one this test asserts: at 1 it fails with "resumed
// delivery Seq = 2" (the intermittent failure R-14.175 recorded), and at 3
// process two finds nothing to deliver and blocks until the test binary's
// ten-minute timeout. Both were reproduced at the W1 hardening gate; an
// instrumented run put the cursor at something other than 2 in 188 of 200
// iterations.
//
// Seeding the state removes the race without weakening an assertion. What
// this test exists to prove is that a restart resumes from the last
// COMMITTED offset and re-delivers nothing before it, and process two
// still checks exactly that. The live path (a running subscription does
// advance the durable cursor) is covered by
// TestEventBusCursorAdvancesOnDelivery; the opposite crash window by
// TestEventBusReplayCursor_UncommittedCursorRedelivers.
func replayCursorProcessOne(ctx context.Context, t *testing.T, dbPath string, clock *testkit.FrozenClock, namespace, cursorName string) {
	t.Helper()
	driver1, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open (process 1): %v", err)
	}
	bus1 := events.New(driver1, clock)

	for i := 0; i < 3; i++ {
		if _, perr := bus1.Publish(ctx, namespace, events.EventKindPluginRegistered, "test", []byte{byte(i)}); perr != nil {
			t.Fatalf("Publish %d: %v", i, perr)
		}
	}
	seedCursor(ctx, t, driver1, namespace, cursorName, 2)
	if cerr := bus1.Close(); cerr != nil {
		t.Fatalf("closing bus1: %v", cerr)
	}
	if cerr := driver1.Close(); cerr != nil {
		t.Fatalf("closing driver1: %v", cerr)
	}
}

// failingStore wraps a provider.Store and fails every Put call with a
// caller-injected error, proving Publish surfaces (never swallows) a
// storage failure as a typed cascade error.
type failingStore struct {
	*storetest.MemStore
	putErr error
}

func (f *failingStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.MemStore.Put(ctx, namespace, key, value)
}

// TestEventBusPublishError is a required check
// (`go test -run TestEventBusPublishError`). It proves Publish failures
// return typed errors from the A-T7 taxonomy and that storage
// unavailability is surfaced to the caller, never swallowed.
func TestEventBusPublishError(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	injected := cascade.New(cascade.KindUnavailable, "injected: backend unreachable")
	store := &failingStore{MemStore: storetest.NewMemStore(), putErr: injected}
	bus := events.New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })

	_, err := bus.Publish(ctx, "ns", "k", "src", []byte("payload"))
	if err == nil {
		t.Fatal("Publish with a failing store returned nil error, want a typed failure")
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		t.Fatalf("Publish error %v carries no taxonomy Kind", err)
	}
	if kind != cascade.KindUnavailable {
		t.Fatalf("Publish error Kind = %s, want %s", kind, cascade.KindUnavailable)
	}

	// Publish after Close is a distinct, also-typed error path.
	if err := bus.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = bus.Publish(ctx, "ns", "k", "src", nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Publish after Close error = %v, want KindUnavailable", err)
	}
}
