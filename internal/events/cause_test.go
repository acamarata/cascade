// Purpose: the in-memory cause table (cause.go): Publish records the
//
//	context's Cause against the event it persisted, unknown events report
//	false, the table keeps only the newest causeTableSize entries, and
//	the persisted record never carries lineage.
//
// Constraints: white-box (package events) so the stored table and the
//
//	persisted bytes are asserted directly, not inferred from an API echo.
package events

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func newCauseBus(t *testing.T) (*Bus, *storetest.MemStore) {
	t.Helper()
	store := storetest.NewMemStore()
	bus := New(store, testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)))
	t.Cleanup(func() { _ = bus.Close() })
	return bus, store
}

// TestPublishRecordsCauseFromContext proves a Cause on the publish context
// is stored for exactly that (namespace, seq), a context without one
// records nothing, and the persisted record is byte-identical to an
// uncaused publish of the same event.
func TestPublishRecordsCauseFromContext(t *testing.T) {
	bus, store := newCauseBus(t)
	want := Cause{RootNamespace: "jobs", RootSeq: 7, Depth: 2}
	ctx := WithCause(context.Background(), want)
	if got, ok := CauseFrom(ctx); !ok || got != want {
		t.Fatalf("CauseFrom = %+v, %v; want %+v", got, ok, want)
	}
	caused, err := bus.Publish(ctx, "ns", "k", "src", []byte("p"))
	if err != nil {
		t.Fatalf("Publish(caused): %v", err)
	}
	plain, err := bus.Publish(context.Background(), "ns", "k", "src", []byte("p"))
	if err != nil {
		t.Fatalf("Publish(plain): %v", err)
	}
	if got, ok := bus.CauseOf("ns", caused.Seq); !ok || got != want {
		t.Fatalf("CauseOf(caused) = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := bus.CauseOf("ns", plain.Seq); ok {
		t.Fatal("an event published without a Cause has one recorded")
	}
	if _, ok := bus.CauseOf("other", caused.Seq); ok {
		t.Fatal("a Cause leaked to the same seq in another namespace")
	}
	if len(bus.causes.entries) != 1 {
		t.Fatalf("cause table holds %d entries, want 1", len(bus.causes.entries))
	}
	a, _ := store.Get(context.Background(), "ns", eventKey(caused.Seq))
	b, _ := store.Get(context.Background(), "ns", eventKey(plain.Seq))
	decA, errA := decodeEvent(a)
	decB, errB := decodeEvent(b)
	if errA != nil || errB != nil {
		t.Fatalf("decode persisted: %v / %v", errA, errB)
	}
	decA.Seq, decB.Seq = 0, 0
	if !bytes.Equal(encodeEvent(decA), encodeEvent(decB)) {
		t.Fatal("the persisted record of a caused event differs from an uncaused one")
	}
}

// TestCauseOfUnknownEventIsFalse covers the empty table, an unpublished
// seq and CauseFrom on a bare context.
func TestCauseOfUnknownEventIsFalse(t *testing.T) {
	bus, _ := newCauseBus(t)
	if _, ok := bus.CauseOf("ns", 1); ok {
		t.Fatal("CauseOf on an empty table reported true")
	}
	ctx := WithCause(context.Background(), Cause{RootNamespace: "ns", RootSeq: 1})
	if _, err := bus.Publish(ctx, "ns", "k", "s", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, ok := bus.CauseOf("ns", 2); ok {
		t.Fatal("CauseOf for a never-published seq reported true")
	}
	if _, ok := CauseFrom(context.Background()); ok {
		t.Fatal("CauseFrom on a bare context reported true")
	}
}

// TestCauseTableBoundedEvictsOldest publishes causeTableSize+1 caused
// events: the first is evicted, the second and the newest survive, and the
// stored table never exceeds its bound.
func TestCauseTableBoundedEvictsOldest(t *testing.T) {
	bus, _ := newCauseBus(t)
	for i := 1; i <= causeTableSize+1; i++ {
		ctx := WithCause(context.Background(), Cause{RootNamespace: "root", RootSeq: uint64(i)})
		if _, err := bus.Publish(ctx, "ns", "k", "s", nil); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	if n := len(bus.causes.entries); n != causeTableSize {
		t.Fatalf("cause table holds %d entries, want %d", n, causeTableSize)
	}
	if _, ok := bus.CauseOf("ns", 1); ok {
		t.Fatal("the oldest entry survived the 4097th insert")
	}
	if c, ok := bus.CauseOf("ns", 2); !ok || c.RootSeq != 2 {
		t.Fatalf("CauseOf(2) = %+v, %v; want the second entry kept", c, ok)
	}
	if c, ok := bus.CauseOf("ns", causeTableSize+1); !ok || c.RootSeq != causeTableSize+1 {
		t.Fatalf("CauseOf(newest) = %+v, %v", c, ok)
	}
}
