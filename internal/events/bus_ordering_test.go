// Purpose: proves the ordering guarantee stated in bus.go's package doc —
//
//	strict per-namespace Seq order for both live Subscribe delivery and
//	Replay, and NO ordering guarantee assumed across different
//	namespaces (they are independent logs). R-14.117-authorized split.
//
// SPORT: internal.events.Bus/ADDED (ordering tests) (P1-E03-W1-S04-T3).
package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestBus_Ordering_PerNamespaceStrict proves every subscriber to one
// namespace observes every event in exactly the Seq order Publish
// assigned, even when Publish calls interleave across two DIFFERENT
// namespaces.
func TestBus_Ordering_PerNamespaceStrict(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	subA, err := bus.Subscribe(ctx, "topic-a", "consumer", 16)
	if err != nil {
		t.Fatalf("Subscribe topic-a: %v", err)
	}
	subB, err := bus.Subscribe(ctx, "topic-b", "consumer", 16)
	if err != nil {
		t.Fatalf("Subscribe topic-b: %v", err)
	}

	const n = 20
	for i := 0; i < n; i++ {
		if _, err := bus.Publish(ctx, "topic-a", "k", "src", []byte{byte(i)}); err != nil {
			t.Fatalf("Publish topic-a[%d]: %v", i, err)
		}
		if _, err := bus.Publish(ctx, "topic-b", "k", "src", []byte{byte(i)}); err != nil {
			t.Fatalf("Publish topic-b[%d]: %v", i, err)
		}
	}

	assertStrictSeqOrder(t, "topic-a", subA.Events, n)
	assertStrictSeqOrder(t, "topic-b", subB.Events, n)
}

func assertStrictSeqOrder(t *testing.T, label string, ch <-chan events.Event, n int) {
	t.Helper()
	var lastSeq uint64
	for i := 0; i < n; i++ {
		ev := <-ch
		if ev.Seq != lastSeq+1 {
			t.Fatalf("%s: event %d Seq = %d, want %d (strict, gapless order)", label, i, ev.Seq, lastSeq+1)
		}
		lastSeq = ev.Seq
	}
}

// TestBus_Replay_OrderedAndComplete proves Replay(offset) returns every
// event with Seq > offset, in order, dropping none.
func TestBus_Replay_OrderedAndComplete(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	const n = 10
	for i := 0; i < n; i++ {
		if _, err := bus.Publish(ctx, "ns", "k", "src", []byte{byte(i)}); err != nil {
			t.Fatalf("Publish[%d]: %v", i, err)
		}
	}

	replayed, err := bus.Replay(ctx, "ns", 4) // exclusive: expect Seq 5..10
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != n-4 {
		t.Fatalf("Replay(4) returned %d events, want %d", len(replayed), n-4)
	}
	for i, ev := range replayed {
		wantSeq := uint64(5 + i)
		if ev.Seq != wantSeq {
			t.Fatalf("Replay(4)[%d] Seq = %d, want %d", i, ev.Seq, wantSeq)
		}
	}

	// Replay(0) is the full history; Replay(n) is empty (offset is
	// exclusive, so equal-to-head means "nothing new").
	full, err := bus.Replay(ctx, "ns", 0)
	if err != nil {
		t.Fatalf("Replay(0): %v", err)
	}
	if len(full) != n {
		t.Fatalf("Replay(0) returned %d events, want %d", len(full), n)
	}
	empty, err := bus.Replay(ctx, "ns", n)
	if err != nil {
		t.Fatalf("Replay(n): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Replay(n) returned %d events, want 0", len(empty))
	}
}
