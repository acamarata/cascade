// Package topics (exemplar_store_test.go): Purpose: ExemplarStore.Add and
// Exemplars, including overflow eviction, the persistence round-trip
// across a fresh instance (the acceptance criterion's "simulated restart"),
// error paths, and construction validation. Backed by
// internal/storage/storetest.MemStore, a real reference provider.Store
// implementation (not a stub - see its own package doc), never a
// self-authored fake of the Store interface.
package topics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fixedClock is a Clock double that always reports the same instant,
// advanceable so a test can prove ordering without depending on wall time.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func newFixedClock() *fixedClock {
	return &fixedClock{now: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}
}

// mustExemplarStore builds an ExemplarStore backed by store or fails the
// test immediately - a shared shortcut for tests whose focus is elsewhere.
func mustExemplarStore(t *testing.T, store provider.Store) *ExemplarStore {
	t.Helper()
	es, err := NewExemplarStore(store, newFixedClock(), 20)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	return es
}

// TestExemplarStoreAddIsIdempotentAcrossTheClock is D7's own case: the
// exemplar id is a content address with no clock in it, so adding the SAME
// turn again after wall time has moved is a no-op, not a second copy of the
// same exemplar under a freshly-minted id.
func TestExemplarStoreAddIsIdempotentAcrossTheClock(t *testing.T) {
	clock := newFixedClock()
	es, err := NewExemplarStore(storetest.NewMemStore(), clock, 20)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	ctx := context.Background()
	turn := Turn{Speaker: "a", Text: "the same turn twice"}
	if err := es.Add(ctx, TopicType("code"), turn); err != nil {
		t.Fatalf("Add (first): %v", err)
	}
	clock.now = clock.now.Add(90 * time.Minute)
	if err := es.Add(ctx, TopicType("code"), turn); err != nil {
		t.Fatalf("Add (second, clock advanced): %v", err)
	}
	got, err := es.Exemplars(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("Exemplars: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Exemplars(code) = %+v after adding one turn twice with the clock advanced between, want exactly one entry", got)
	}
}

// TestExemplarStoreRefusesUnusableTopicType is the key-space boundary on
// both methods: a TopicType that could not be a safe storage key is refused
// rather than silently rewritten into one.
func TestExemplarStoreRefusesUnusableTopicType(t *testing.T) {
	es := mustExemplarStore(t, storetest.NewMemStore())
	ctx := context.Background()
	for _, bad := range []TopicType{"", "has space", "slash/es", TopicType(strings.Repeat("x", topicTypeMaxLen+1))} {
		if err := es.Add(ctx, bad, Turn{Speaker: "a", Text: "x"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Add(%q) error = %v, want KindInvalidInput", bad, err)
		}
		if _, err := es.Exemplars(ctx, bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Exemplars(%q) error = %v, want KindInvalidInput", bad, err)
		}
	}
}

func TestExemplarStoreAddThenExemplarsRoundTrip(t *testing.T) {
	store := storetest.NewMemStore()
	clock := newFixedClock()
	es, err := NewExemplarStore(store, clock, 20)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	turn := Turn{Speaker: "a", Text: "hello"}
	if err := es.Add(context.Background(), TopicType("code"), turn); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := es.Exemplars(context.Background(), TopicType("code"))
	if err != nil {
		t.Fatalf("Exemplars: %v", err)
	}
	if len(got) != 1 || got[0] != turn {
		t.Fatalf("Exemplars(code) = %+v, want [%+v]", got, turn)
	}
}

// TestExemplarStorePersistsAcrossFreshInstance is the acceptance
// criterion's own scenario: write via Add, then read via Exemplars from a
// SECOND ExemplarStore backed by the same MemStore (simulating a restart -
// nothing in this package's own process state survives, only the store).
func TestExemplarStorePersistsAcrossFreshInstance(t *testing.T) {
	backing := storetest.NewMemStore()
	clock := newFixedClock()
	first, err := NewExemplarStore(backing, clock, 20)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	turn := Turn{Speaker: "b", Text: "restart me"}
	if err := first.Add(context.Background(), TopicType("memory"), turn); err != nil {
		t.Fatalf("Add: %v", err)
	}

	second, err := NewExemplarStore(backing, clock, 20)
	if err != nil {
		t.Fatalf("NewExemplarStore (fresh instance): %v", err)
	}
	got, err := second.Exemplars(context.Background(), TopicType("memory"))
	if err != nil {
		t.Fatalf("Exemplars (fresh instance): %v", err)
	}
	if len(got) != 1 || got[0] != turn {
		t.Fatalf("fresh-instance Exemplars(memory) = %+v, want [%+v] (persistence did not survive the simulated restart)", got, turn)
	}
}

// TestExemplarStoreOverflowEvictsOldest proves the bounded FIFO actually
// bounds: adding one more than maxDepth must drop the OLDEST entry, never
// the newest, and Exemplars must never return more than maxDepth entries.
func TestExemplarStoreOverflowEvictsOldest(t *testing.T) {
	store := storetest.NewMemStore()
	es, err := NewExemplarStore(store, newFixedClock(), 2)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	ctx := context.Background()
	for i, text := range []string{"first", "second", "third"} {
		if err := es.Add(ctx, TopicType("code"), Turn{Speaker: "a", Text: text}); err != nil {
			t.Fatalf("Add(%d, %q): %v", i, text, err)
		}
	}
	got, err := es.Exemplars(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("Exemplars: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Exemplars returned %d entries with maxDepth 2, want exactly 2", len(got))
	}
	if got[0].Text != "second" || got[1].Text != "third" {
		t.Fatalf("Exemplars = %+v, want [second, third] (oldest \"first\" evicted, newest kept)", got)
	}
}

func TestExemplarStoreExemplarsEmptyTopicReturnsNil(t *testing.T) {
	store := storetest.NewMemStore()
	es, err := NewExemplarStore(store, newFixedClock(), 20)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	got, err := es.Exemplars(context.Background(), TopicType("never-added"))
	if err != nil {
		t.Fatalf("Exemplars(never-added): %v", err)
	}
	if got != nil {
		t.Fatalf("Exemplars(never-added) = %v, want nil", got)
	}
}

// TestExemplarStoreDefaultDepth proves maxDepth<=0 falls back to
// defaultExemplarDepth (20) rather than producing an unbounded or
// zero-capacity store.
func TestExemplarStoreDefaultDepth(t *testing.T) {
	store := storetest.NewMemStore()
	es, err := NewExemplarStore(store, newFixedClock(), 0)
	if err != nil {
		t.Fatalf("NewExemplarStore(maxDepth=0): %v", err)
	}
	if es.maxDepth != defaultExemplarDepth {
		t.Fatalf("maxDepth = %d, want the default %d", es.maxDepth, defaultExemplarDepth)
	}
}

func TestNewExemplarStoreRejectsNilDependencies(t *testing.T) {
	if _, err := NewExemplarStore(nil, newFixedClock(), 20); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewExemplarStore(nil store, ...) error = %v, want KindInvalidInput", err)
	}
	if _, err := NewExemplarStore(storetest.NewMemStore(), nil, 20); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewExemplarStore(..., nil clock, ...) error = %v, want KindInvalidInput", err)
	}
}

func TestExemplarStoreAddRejectsNilContext(t *testing.T) {
	es, _ := NewExemplarStore(storetest.NewMemStore(), newFixedClock(), 20)
	//nolint:staticcheck // deliberate nil ctx to prove the guard fires
	if err := es.Add(nil, TopicType("code"), Turn{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Add(nil ctx, ...) error = %v, want KindInvalidInput", err)
	}
}

func TestExemplarStoreExemplarsRejectsNilContext(t *testing.T) {
	es, _ := NewExemplarStore(storetest.NewMemStore(), newFixedClock(), 20)
	//nolint:staticcheck // deliberate nil ctx to prove the guard fires
	if _, err := es.Exemplars(nil, TopicType("code")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Exemplars(nil ctx, ...) error = %v, want KindInvalidInput", err)
	}
}

// TestExemplarStoreLoadDecodeErrorPropagates is the mutation-facing proof
// for load's decode branch: corrupt bytes under the exact key Add/Exemplars
// read must surface as a typed KindIntegrity error from both callers, never
// a panic and never a silently-empty result.
func TestExemplarStoreLoadDecodeErrorPropagates(t *testing.T) {
	backing := storetest.NewMemStore()
	if err := backing.Put(context.Background(), exemplarNamespace, exemplarKey(TopicType("code")), []byte("not json")); err != nil {
		t.Fatalf("seeding corrupt record: %v", err)
	}
	es, err := NewExemplarStore(backing, newFixedClock(), 20)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	if _, err := es.Exemplars(context.Background(), TopicType("code")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Exemplars over corrupt bytes: error = %v, want KindIntegrity", err)
	}
	if err := es.Add(context.Background(), TopicType("code"), Turn{Speaker: "a", Text: "x"}); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Add over corrupt bytes: error = %v, want KindIntegrity (load must run before write)", err)
	}
}
