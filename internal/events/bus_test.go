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

// replayCursorProcessOne publishes 3 events, consumes 2, then vanishes
// without calling Unsubscribe or Close — a kill -9, not a clean shutdown —
// leaving its 3rd published event undelivered.
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
	sub1, err := bus1.Subscribe(ctx, namespace, cursorName, 8)
	if err != nil {
		t.Fatalf("Subscribe (process 1): %v", err)
	}
	for i := 0; i < 2; i++ {
		ev := <-sub1.Events
		if ev.Seq != uint64(i+1) {
			t.Fatalf("process 1 event %d Seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
	if err := driver1.Close(); err != nil {
		t.Fatalf("closing driver1: %v", err)
	}
}

// replayCursorProcessTwo opens a fresh Bus/Driver over the SAME database
// file — the "restart" — and proves the durable cursor resumes exactly
// where process one left off, and that Publish sequence numbering
// recovered correctly too.
func replayCursorProcessTwo(ctx context.Context, t *testing.T, dbPath string, clock *testkit.FrozenClock, namespace, cursorName string) {
	t.Helper()
	driver2, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open (process 2): %v", err)
	}
	t.Cleanup(func() { _ = driver2.Close() })
	bus2 := events.New(driver2, clock)
	t.Cleanup(func() { _ = bus2.Close() })

	sub2, err := bus2.Subscribe(ctx, namespace, cursorName, 8)
	if err != nil {
		t.Fatalf("Subscribe (process 2): %v", err)
	}
	select {
	case ev, ok := <-sub2.Events:
		if !ok {
			t.Fatal("process 2 Events closed with no event delivered")
		}
		if ev.Seq != 3 {
			t.Fatalf("resumed delivery Seq = %d, want 3 (no re-delivery of already-committed 1,2)", ev.Seq)
		}
	case err := <-sub2.Errs:
		t.Fatalf("unexpected Errs receive: %v", err)
	}

	next, err := bus2.Publish(ctx, namespace, events.EventKindPluginRegistered, "test", nil)
	if err != nil {
		t.Fatalf("Publish after restart: %v", err)
	}
	if next.Seq != 4 {
		t.Fatalf("post-restart Publish Seq = %d, want 4", next.Seq)
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
