package ci

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// scanFailStore is a MemStore whose Scan always fails, so a replay of the
// event store cannot be read.
type scanFailStore struct{ *storetest.MemStore }

func (scanFailStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, errors.New("scan refused")
}

func TestWatchReplayEndIsLastCheckpointSeq(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	handler := WatchReplayEndHandler(bus)

	got, err := handler(ctx, nil)
	if err != nil {
		t.Fatalf("empty store: %v", err)
	}
	if end := got.(map[string]uint64)["end"]; end != 0 {
		t.Fatalf("empty store end = %d, want 0", end)
	}

	var want uint64
	for _, kind := range []events.EventKind{EventKindCheckpointPlanned, EventKindCheckpointDispatched, EventKindCheckpointTerminal, EventKindCheckpointAborted} {
		e, err := bus.Publish(ctx, EventNamespace, kind, "t", []byte(`{}`))
		if err != nil {
			t.Fatalf("Publish(%s): %v", kind, err)
		}
		want = e.Seq
	}
	// A later non-watch kind in the same namespace must not move the end.
	if _, err := bus.Publish(ctx, EventNamespace, EventKindRunCompleted, "t", []byte(`{}`)); err != nil {
		t.Fatalf("Publish(run completed): %v", err)
	}

	got, err = handler(ctx, nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if end := got.(map[string]uint64)["end"]; end != want {
		t.Fatalf("end = %d, want %d (the last checkpoint event)", end, want)
	}
	if NewWatchSSEHandler(bus, clock) == nil {
		t.Fatal("NewWatchSSEHandler returned nil")
	}
}

func TestWatchReplayEndUnreadableStoreIsUnavailable(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(scanFailStore{storetest.NewMemStore()}, clock)
	_, err := WatchReplayEndHandler(bus)(context.Background(), nil)
	if !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}
