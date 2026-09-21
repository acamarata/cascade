// Package topics (observe_state_test.go): the first-use window's own
// behavior - the LITERAL storage coordinates of the state row, IsObserving's
// three real outcomes (absent, open, unreadable), first_use_at's idempotence
// across instances, the conditional create's failure path, and the race a
// second instance loses. Helpers and store doubles come from
// observe_doubles_test.go; see observe_log_test.go's header for the rest.
package topics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestObserveStateRowIsTheContractRow pins the R-16.63-addendum coordinates
// as LITERAL strings and then proves the write really lands on them.
//
// Every other test in this ticket seeds and reads through
// observeStateNamespace/observeStateKey, which ratifies whatever those
// constants happen to say: redefining them to another domain and another key
// left the whole suite green (CR finding 1). The literals below, and the
// literal-coordinate read after a real Observe, are what make that a red test.
func TestObserveStateRowIsTheContractRow(t *testing.T) {
	wantEq(t, observeStateNamespace, "retrieval", "observe state namespace")
	wantEq(t, observeStateKey, "topic_observe_state", "observe state key")
	wantEq(t, observeStateNamespace, string(storage.DomainRetrieval), "namespace is the retrieval domain id")
	clock := newFixedClock()
	mem := storetest.NewMemStore()
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), newFakeThreadStore()),
		mem, clock, &fakePublisher{})
	if _, err := ol.Observe(context.Background(), observeTurns()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	raw, err := mem.Get(context.Background(), "retrieval", "topic_observe_state")
	if err != nil {
		t.Fatalf(`reading the literal row ("retrieval", "topic_observe_state") back: %v`, err)
	}
	var rec observeStateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decoding the state row: %v", err)
	}
	wantEq(t, rec.TopicType, "engine", "the row's topic_type field")
	wantEq(t, rec.FirstUseAt, clock.now.UTC().Format(time.RFC3339), "the row's first_use_at field")
}

// TestObserveLoggerIsObservingFallbackRead exercises IsObserving's own
// no-ctx store read across the row's four real states. An absent row is the
// AC's "no Observe call has occurred" and is NOT an error; a store that
// cannot answer and a row that will not decode are errors with distinct
// kinds, never the safe-looking "absent" (CR finding 10).
func TestObserveLoggerIsObservingFallbackRead(t *testing.T) {
	clock := newFixedClock()
	at := mustAutoThreader(t, &fakeSegmenter{}, newFakeThreadStore())
	cases := []struct {
		name     string
		store    func(t *testing.T) provider.Store
		want     bool
		wantKind cascade.Kind // 0 means "want a nil error"
	}{
		{"absent row", func(*testing.T) provider.Store { return storetest.NewMemStore() }, false, 0},
		{"row present at elapsed 0", func(t *testing.T) provider.Store {
			s := storetest.NewMemStore()
			seedFirstUseAt(t, s, clock.now)
			return s
		}, true, 0},
		{"row present past the window", func(t *testing.T) provider.Store {
			s := storetest.NewMemStore()
			seedFirstUseAt(t, s, clock.now.Add(-observeWindow))
			return s
		}, false, 0},
		{"store cannot answer", func(*testing.T) provider.Store {
			return &onceStore{Store: storetest.NewMemStore(), limit: 0}
		}, false, cascade.KindUnavailable},
		{"corrupt row", func(t *testing.T) provider.Store {
			s := storetest.NewMemStore()
			putRaw(t, s, []byte("not json"))
			return s
		}, false, cascade.KindIntegrity},
		{"unparseable first_use_at", func(t *testing.T) provider.Store {
			s := storetest.NewMemStore()
			putRaw(t, s, []byte(`{"topic_type":"engine","first_use_at":"yesterday"}`))
			return s
		}, false, cascade.KindIntegrity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ol := mustObserveLogger(t, at, c.store(t), clock, &fakePublisher{})
			got, err := ol.IsObserving()
			wantEq(t, got, c.want, "IsObserving()")
			if c.wantKind == 0 {
				if err != nil {
					t.Fatalf("IsObserving() error = %v, want nil", err)
				}
				return
			}
			if !cascade.HasKind(err, c.wantKind) {
				t.Fatalf("IsObserving() error = %v, want kind %v", err, c.wantKind)
			}
		})
	}
}

// TestObserveLoggerFirstUseAtIdempotent: a second Observe on the SAME
// instance serves the cache (onceStore's call limit proves it); a fresh
// instance sharing the same backing store reads rather than overwrites.
func TestObserveLoggerFirstUseAtIdempotent(t *testing.T) {
	mem := storetest.NewMemStore()
	store := &onceStore{Store: mem, limit: 2} // 1 Get (KindNotFound) + 1 Tx, first Observe only
	clock := newFixedClock()
	at := mustAutoThreader(t, &fakeSegmenter{}, newFakeThreadStore())
	ol1 := mustObserveLogger(t, at, store, clock, &fakePublisher{})
	turns := []Turn{{Speaker: "a", Text: "seed"}}
	if _, err := ol1.Observe(context.Background(), turns); err != nil {
		t.Fatalf("first Observe: %v", err)
	}
	firstRaw := getRaw(t, mem)
	if _, err := ol1.Observe(context.Background(), turns); err != nil {
		t.Fatalf("second Observe (same instance): %v (cache should have served it)", err)
	}
	wantEq(t, store.calls, 2, "store touched (want 2: cache should serve the second call)")
	clock.now = clock.now.Add(2 * time.Hour)
	ol2 := mustObserveLogger(t, at, mem, clock, &fakePublisher{}) // fresh instance, empty cache
	if _, err := ol2.Observe(context.Background(), []Turn{{Speaker: "a", Text: "seed-again"}}); err != nil {
		t.Fatalf("third Observe (fresh instance): %v", err)
	}
	wantEq(t, string(getRaw(t, mem)), string(firstRaw), "first_use_at changed by a later Observe")
}

// TestObserveLoggerInitFirstUseAtFailurePropagates: the conditional create
// runs inside the store's transaction, and a store that refuses the
// transaction fails the Observe rather than proceeding on an unrecorded
// window (CR finding 7: this path was untested).
func TestObserveLoggerInitFirstUseAtFailurePropagates(t *testing.T) {
	pub := &fakePublisher{}
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), newFakeThreadStore()),
		refusingStore{}, newFixedClock(), pub)
	got, err := ol.Observe(context.Background(), observeTurns())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Observe with a store that refuses the init transaction: error = %v, want KindUnavailable", err)
	}
	wantEq(t, got.Observed, false, "ObserveResult.Observed after a failed init")
	wantEq(t, len(pub.calls), 0, "audit events published after a failed init")
}

// TestObserveLoggerFirstUseAtRaceAdoptsTheWinner is the never-overwritten
// guarantee under the interleaving that would break a plain Get-then-Put
// (CR finding 12): this instance's read sees no row, a racing instance
// commits one, and the conditional create then conflicts. The window must
// come back as the WINNER's first_use_at, and the stored row must be
// untouched - not moved forward to this instance's clock.
func TestObserveLoggerFirstUseAtRaceAdoptsTheWinner(t *testing.T) {
	clock := newFixedClock()
	mem := storetest.NewMemStore()
	winner := clock.now.Add(-72 * time.Hour)
	seedFirstUseAt(t, mem, winner)
	before := string(getRaw(t, mem))
	pub := &fakePublisher{}
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), newFakeThreadStore()),
		&racedStore{Store: mem}, clock, pub)
	got, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Observe after losing the init race: %v", err)
	}
	wantEq(t, got.Observed, true, "ObserveResult.Observed (72h in, still observing)")
	wantEq(t, len(pub.calls), 1, "audit events published")
	evt := decodeObserveEvent(t, pub.calls[0].payload)
	wantEq(t, evt.ElapsedDays, 3.0, "ElapsedDays measured from the winner's first_use_at, not this instance's clock")
	wantEq(t, string(getRaw(t, mem)), before, "the stored row was rewritten by the losing instance")
}

// TestObserveLoggerRaceAdoptionPropagatesAUnreadableRow: losing the race and
// then failing to read the winner's row is a failure, not a silent restart of
// the window.
func TestObserveLoggerRaceAdoptionPropagatesAUnreadableRow(t *testing.T) {
	mem := storetest.NewMemStore()
	putRaw(t, mem, []byte("not json"))
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), newFakeThreadStore()),
		&racedStore{Store: mem}, newFixedClock(), &fakePublisher{})
	if _, err := ol.Observe(context.Background(), observeTurns()); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Observe after losing the race to an unreadable row: error = %v, want KindIntegrity", err)
	}
}

// putRaw writes value at the state row's coordinates; getRaw reads it back.
func putRaw(t *testing.T, store provider.Store, value []byte) {
	t.Helper()
	if err := store.Put(context.Background(), observeStateNamespace, observeStateKey, value); err != nil {
		t.Fatalf("seeding the state row: %v", err)
	}
}

func getRaw(t *testing.T, store provider.Store) []byte {
	t.Helper()
	raw, err := store.Get(context.Background(), observeStateNamespace, observeStateKey)
	if err != nil {
		t.Fatalf("reading the state row: %v", err)
	}
	return raw
}
