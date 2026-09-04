package audit

// Purpose: every error path the write and read surfaces can take, an
//   invalid event, an unavailable store on each of its three entry
//   points, and a commit that succeeded while its bus notification did
//   not.
// Constraints: Art.7.1, Art.7.3, Art.11 as in log_test.go.
// SPORT: internal.audit.Log/ADDED (tests) (P1-E09-W2-S18-T2).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// failingStore fails one named operation and delegates the rest, so each
// error path can be driven separately.
type failingStore struct {
	provider.Store
	failGet  bool
	failScan bool
	failTx   bool
}

func (f *failingStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if f.failGet {
		return nil, cascade.New(cascade.KindUnavailable, "store down")
	}
	return f.Store.Get(ctx, ns, key)
}

func (f *failingStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	if f.failScan {
		return nil, cascade.New(cascade.KindUnavailable, "store down")
	}
	return f.Store.Scan(ctx, ns, prefix)
}

func (f *failingStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	if f.failTx {
		return cascade.New(cascade.KindUnavailable, "store down")
	}
	return f.Store.Tx(ctx, fn)
}

// failingBus reports a publish failure.
type failingBus struct{}

func (failingBus) Publish(context.Context, string, events.EventKind, string, []byte) (events.Event, error) {
	return events.Event{}, cascade.New(cascade.KindUnavailable, "bus down")
}

func TestAuditErrorPaths(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(testInstant)

	t.Run("invalid event", func(t *testing.T) {
		log := New(storetest.NewMemStore(), clock, nil)
		if _, err := log.Append(ctx, Event{Kind: Kind("nope"), Actor: "u", Action: "a"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Append with an unknown kind: %v, want KindInvalidInput", err)
		}
	})
	t.Run("store write unavailable", func(t *testing.T) {
		log := New(&failingStore{Store: storetest.NewMemStore(), failTx: true}, clock, nil)
		if _, err := log.Append(ctx, sampleEvent(1)); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Append against a failed store: %v, want KindUnavailable", err)
		}
	})
	t.Run("head read unavailable", func(t *testing.T) {
		log := New(&failingStore{Store: storetest.NewMemStore(), failGet: true}, clock, nil)
		if _, err := log.Append(ctx, sampleEvent(1)); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Append with an unreadable head: %v, want KindUnavailable", err)
		}
	})
	t.Run("scan unavailable", func(t *testing.T) {
		log := New(&failingStore{Store: storetest.NewMemStore(), failScan: true}, clock, nil)
		if _, err := log.Query(ctx, Filter{}); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Query against a failed store: %v, want KindUnavailable", err)
		}
	})
	t.Run("committed but not notified", func(t *testing.T) {
		store := storetest.NewMemStore()
		log := New(store, clock, failingBus{})
		rec, err := log.Append(ctx, sampleEvent(1))
		if err == nil {
			t.Fatal("a failed notification was reported as success")
		}
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("notification failure: %v, want KindUnavailable", err)
		}
		if rec.Seq != 1 {
			t.Fatal("the committed record was not returned alongside the notification error")
		}
		reader := New(store, clock, nil)
		if page, qerr := reader.Query(ctx, Filter{}); qerr != nil || len(page.Records) != 1 {
			t.Fatalf("the record is not in the log: %d records, err %v", len(page.Records), qerr)
		}
	})
}

// TestAuditQueryEmptyLog covers the boundary the tamper checks must NOT
// fire on: a log with no records at all and therefore no tail pointer.
func TestAuditQueryEmptyLog(t *testing.T) {
	ctx := context.Background()
	log := New(storetest.NewMemStore(), testkit.NewFrozenClock(testInstant), nil)
	page, err := log.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query on an empty log: %v", err)
	}
	if len(page.Records) != 0 || page.NextCursor != "" {
		t.Fatalf("Query on an empty log returned %+v", page)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify on an empty log: %v", err)
	}
}

// TestAuditQueryHeadUnavailable covers the store failing on the tail-
// pointer read that closes a completed walk. It must refuse, not decide
// the log simply ends where the scan stopped.
func TestAuditQueryHeadUnavailable(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testInstant)
	if _, err := New(store, clock, nil).Append(ctx, sampleEvent(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	reader := New(&failingStore{Store: store, failGet: true}, clock, nil)
	if _, err := reader.Query(ctx, Filter{}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Query with an unreadable tail pointer: %v, want KindUnavailable", err)
	}
}
