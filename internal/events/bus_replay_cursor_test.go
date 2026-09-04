// Purpose: replay-cursor coverage split out of bus_test.go under
//
//	R-14.117's authorized-split allowance (Art.10.3's 300-line cap):
//	the cursor seed/read helpers, the live cursor-advance test, and the
//	at-least-once redelivery test that pins the opposite interleaving.
//
// Constraints: Art.7.1 - every path is under t.TempDir(); Art.11 - no
//
//	sleep is used as a synchronization primitive.
//
// SPORT: internal.events.Bus/CHANGED (replay-cursor race coverage) (P1-E04-W1-S07-T7).
package events_test

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// seedCursor writes name's durable cursor record in namespace to seq,
// through the same Store API and the same key layout the bus itself uses
// (cursor.go's cursorKeyPrefix + big-endian uint64). It exists so a test
// can state the exact resume precondition it means to exercise instead of
// racing a delivery goroutine into producing it.
func seedCursor(ctx context.Context, t *testing.T, store provider.Store, namespace, name string, seq uint64) {
	t.Helper()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	if err := store.Put(ctx, namespace, "cursor:"+name, buf[:]); err != nil {
		t.Fatalf("seeding cursor %q in namespace %q to %d: %v", name, namespace, seq, err)
	}
}

// readCursor returns name's durable cursor value, or 0 when no record
// exists yet ("open-or-create at Seq 0", cursor.go).
func readCursor(ctx context.Context, t *testing.T, store provider.Store, namespace, name string) uint64 {
	t.Helper()
	raw, err := store.Get(ctx, namespace, "cursor:"+name)
	if err != nil {
		return 0
	}
	if len(raw) != 8 {
		t.Fatalf("cursor %q in namespace %q has a malformed value (%d bytes)", name, namespace, len(raw))
	}
	return binary.BigEndian.Uint64(raw)
}

// TestEventBusCursorAdvancesOnDelivery covers the live path the seeded
// replay test deliberately does not: a running subscription really does
// advance the durable cursor, from 0 to the last event delivered.
//
// It consumes ALL three events before closing, on purpose. Consuming only
// some of them would make the final cursor value a race: the delivery loop
// selects between sending the next event and observing the done channel,
// and with a buffered subscription channel both are ready at once, so a
// partially-drained subscription can stop at any committed offset. Closing
// the bus after the last receive is a defined synchronization point (Close
// signals each subscription's done channel and blocks until its delivery
// goroutine has exited), so the value read afterwards is settled.
func TestEventBusCursorAdvancesOnDelivery(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	const namespace = "queue"
	const cursorName = "consumer-a"

	driver, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	bus := events.New(driver, clock)

	for i := 0; i < 3; i++ {
		if _, perr := bus.Publish(ctx, namespace, events.EventKindPluginRegistered, "test", []byte{byte(i)}); perr != nil {
			t.Fatalf("Publish %d: %v", i, perr)
		}
	}
	if before := readCursor(ctx, t, driver, namespace, cursorName); before != 0 {
		t.Fatalf("cursor before any subscription = %d, want 0", before)
	}

	sub, err := bus.Subscribe(ctx, namespace, cursorName, 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for i := 0; i < 3; i++ {
		ev := <-sub.Events
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d Seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
	if cerr := bus.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	if got := readCursor(ctx, t, driver, namespace, cursorName); got != 3 {
		t.Fatalf("cursor after delivering all 3 events = %d, want 3", got)
	}
}

// TestEventBusReplayCursor_UncommittedCursorRedelivers pins the OTHER
// side of the interleaving the flake exposed, so neither direction is
// left to chance.
//
// The bus commits a cursor only after the event is handed to the
// subscriber, so a process that dies in that window leaves the cursor
// behind the event it already delivered. The documented guarantee is
// at-least-once: the restart MUST re-deliver that event rather than skip
// it. This test writes the cursor at 1 directly, restarts, and asserts
// event 2 arrives again. A change that "fixed" the flake by committing
// the cursor before delivery would turn this test red, which is the
// point: that change would silently make the bus at-most-once and lose
// events on a crash.
func TestEventBusReplayCursor_UncommittedCursorRedelivers(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	const namespace = "queue"
	const cursorName = "consumer-a"

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
	// The state a crash between the send of event 2 and its cursor
	// commit leaves behind: two events delivered, cursor still at 1.
	seedCursor(ctx, t, driver1, namespace, cursorName, 1)
	if cerr := driver1.Close(); cerr != nil {
		t.Fatalf("closing driver1: %v", cerr)
	}

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
		if ev.Seq != 2 {
			t.Fatalf("resumed delivery Seq = %d, want 2 (at-least-once: an event delivered but not committed is re-delivered)", ev.Seq)
		}
	case err := <-sub2.Errs:
		t.Fatalf("unexpected Errs receive: %v", err)
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
