// Purpose: white-box unit tests for loadCursor/commitCursor in isolation
//
//	from Bus — the "cursor miss" and malformed-value error paths task 6
//	calls for, plus the open-or-create-at-0 contract (task 3). In package
//	events (not events_test) because loadCursor/commitCursor are
//	unexported. R-14.117-authorized split from bus_test.go.
//
// SPORT: internal.events.Bus/ADDED (cursor tests) (P1-E03-W1-S04-T3).
package events

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLoadCursor_OpenOrCreateAtZero(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	seq, err := loadCursor(ctx, store, "ns", "never-committed")
	if err != nil {
		t.Fatalf("loadCursor for a never-committed name returned an error: %v", err)
	}
	if seq != 0 {
		t.Fatalf("loadCursor for a never-committed name = %d, want 0", seq)
	}
}

func TestCommitCursor_ThenLoad_RoundTrips(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	if err := commitCursor(ctx, store, "ns", "consumer-a", 42); err != nil {
		t.Fatalf("commitCursor: %v", err)
	}
	seq, err := loadCursor(ctx, store, "ns", "consumer-a")
	if err != nil {
		t.Fatalf("loadCursor: %v", err)
	}
	if seq != 42 {
		t.Fatalf("loadCursor after commit = %d, want 42", seq)
	}

	// A later commit overwrites, never appends or requires the prior
	// value.
	if err := commitCursor(ctx, store, "ns", "consumer-a", 43); err != nil {
		t.Fatalf("second commitCursor: %v", err)
	}
	seq, err = loadCursor(ctx, store, "ns", "consumer-a")
	if err != nil {
		t.Fatalf("loadCursor after second commit: %v", err)
	}
	if seq != 43 {
		t.Fatalf("loadCursor after second commit = %d, want 43", seq)
	}
}

func TestLoadCursor_MalformedValue(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := store.Put(ctx, "ns", cursorKey("consumer-a"), []byte("short")); err != nil {
		t.Fatalf("seeding malformed cursor: %v", err)
	}

	_, err := loadCursor(ctx, store, "ns", "consumer-a")
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("loadCursor on malformed value = %v, want KindIntegrity", err)
	}
}

// cursorMissStore fails every Get with a non-NotFound error, proving
// loadCursor distinguishes a genuine "never committed" miss (KindNotFound,
// handled as 0/nil) from a real storage failure (surfaced, not swallowed).
type cursorMissStore struct {
	*storetest.MemStore
	getErr error
}

func (s *cursorMissStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.MemStore.Get(ctx, namespace, key)
}

func TestLoadCursor_StorageUnavailable(t *testing.T) {
	ctx := context.Background()
	injected := cascade.New(cascade.KindUnavailable, "injected: backend unreachable")
	store := &cursorMissStore{MemStore: storetest.NewMemStore(), getErr: injected}

	_, err := loadCursor(ctx, store, "ns", "consumer-a")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("loadCursor with a failing store = %v, want KindUnavailable", err)
	}
}

func TestUnsubscribe_CursorMiss(t *testing.T) {
	ctx := context.Background()
	bus := New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)))
	t.Cleanup(func() { _ = bus.Close() })

	err := bus.Unsubscribe("ns", "never-subscribed")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Unsubscribe on an unknown cursor = %v, want KindNotFound", err)
	}

	sub, subErr := bus.Subscribe(ctx, "ns", "consumer-a", 1)
	if subErr != nil {
		t.Fatalf("Subscribe: %v", subErr)
	}
	if unsubErr := sub.Unsubscribe(); unsubErr != nil {
		t.Fatalf("first Unsubscribe: %v", unsubErr)
	}
	// A second Unsubscribe of the same, now-inactive name is a miss too.
	if unsubErr := sub.Unsubscribe(); !cascade.HasKind(unsubErr, cascade.KindNotFound) {
		t.Fatalf("second Unsubscribe = %v, want KindNotFound", unsubErr)
	}
}
