// Package topics (auto_thread_test.go): Purpose: AutoThreader.Route's
// boundary-to-thread routing, new-topic creation, taxonomy-label mapping,
// boundary-list validation, re-delivery idempotence, Segmenter/ThreadStore
// error propagation, and NewDefaultAutoThreader's real NewSegmenter wiring.
// fakeSegmenter is this file's own double; fakeThreadStore comes from
// thread_store_test.go, fixedClock from exemplar_store_test.go,
// fakePublisher from reassign_test.go, and
// fakeClassifyExecutor/fakeEmbedder/turnsN/validCfg from
// segmenter_core_test.go, all in this same package. The error-propagation
// and constructor-refusal tests are in auto_thread_errors_test.go.
package topics

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSegmenter is a deterministic Segmenter double: it returns a
// caller-configured []Boundary (or error) and records the turns it was
// last called with, so a test can assert AutoThreader.Route passed its own
// input through unmodified.
type fakeSegmenter struct {
	boundaries []Boundary
	err        error
	calls      int
	gotTurns   []Turn
}

func (f *fakeSegmenter) Segment(_ context.Context, turns []Turn) ([]Boundary, error) {
	f.calls++
	f.gotTurns = turns
	if f.err != nil {
		return nil, f.err
	}
	return f.boundaries, nil
}

var _ Segmenter = (*fakeSegmenter)(nil)

func testTaxonomy() TaxonomyConfig {
	return NewTaxonomyConfig(map[string]TopicType{"code": TopicType("code-topic")}, TopicType("fallback"))
}

// mustAutoThreader wires a ready AutoThreader over seg and store with this
// package's standard test taxonomy, a real MemStore-backed ExemplarStore,
// and fixed clock - the shape every test here needs, so the constructor's
// six dependencies are named once rather than at each call site.
func mustAutoThreader(t *testing.T, seg Segmenter, store ThreadStore) *AutoThreader {
	t.Helper()
	at, err := NewAutoThreader(seg, store, testTaxonomy(),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	return at
}

// TestAutoThreaderRouteNoBoundariesIsOneFallbackSegment covers the
// turn-zero-has-no-label case this file's sibling auto_thread.go documents:
// zero boundaries means the whole window is one segment whose label is "",
// which testTaxonomy resolves to its fallback.
func TestAutoThreaderRouteNoBoundariesIsOneFallbackSegment(t *testing.T) {
	seg := &fakeSegmenter{}
	store := newFakeThreadStore()
	at := mustAutoThreader(t, seg, store)
	turns := []Turn{{Speaker: "a", Text: "one"}, {Speaker: "b", Text: "two"}, {Speaker: "a", Text: "three"}}
	threadIDs, err := at.Route(context.Background(), turns)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(threadIDs) != 1 {
		t.Fatalf("Route returned %d thread ids, want 1", len(threadIDs))
	}
	if got := store.threads[TopicType("fallback")]; got != threadIDs[0] {
		t.Fatalf("fallback topic's thread = %q, want the returned thread id %q", got, threadIDs[0])
	}
	if got := store.texts(threadIDs[0]); len(got) != 3 {
		t.Fatalf("appended %d turns to the single thread, want all 3", len(got))
	}
}

// TestAutoThreaderRouteBoundarySplitsAndMapsTaxonomy is the boundary->
// thread, new-topic-creation, and taxonomy-mapping proof together: two
// segments, one carrying an unmapped label (falls back), one carrying a
// label testTaxonomy maps to a real topic.
func TestAutoThreaderRouteBoundarySplitsAndMapsTaxonomy(t *testing.T) {
	seg := &fakeSegmenter{boundaries: []Boundary{{TurnIndex: 2, Label: "code"}}}
	store := newFakeThreadStore()
	at := mustAutoThreader(t, seg, store)
	turns := []Turn{
		{Speaker: "a", Text: "zero"}, {Speaker: "b", Text: "one"},
		{Speaker: "a", Text: "two"}, {Speaker: "b", Text: "three"},
	}
	threadIDs, err := at.Route(context.Background(), turns)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(threadIDs) != 2 {
		t.Fatalf("Route returned %d thread ids, want 2 (one per segment)", len(threadIDs))
	}
	fallbackThread, ok := store.threads[TopicType("fallback")]
	if !ok {
		t.Fatalf("no thread created for the fallback topic (segment-zero's empty label)")
	}
	codeThread, ok := store.threads[TopicType("code-topic")]
	if !ok {
		t.Fatalf("no thread created for taxonomy-mapped topic %q", "code-topic")
	}
	if fallbackThread == codeThread {
		t.Fatalf("fallback and code-topic resolved to the same thread %q, want two distinct threads", fallbackThread)
	}
	if got := store.texts(fallbackThread); len(got) != 2 || got[0] != "zero" || got[1] != "one" {
		t.Fatalf("fallback thread turns = %v, want [zero one]", got)
	}
	if got := store.texts(codeThread); len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("code-topic thread turns = %v, want [two three]", got)
	}
}

// TestAutoThreaderRouteEmptyTurnsNeverCallsSegmenter is the empty-window
// short circuit: there is nothing to segment, so the Segmenter must not be
// reached at all (a classify/embed round trip for a zero-turn window is
// pure waste), and no thread may be created.
func TestAutoThreaderRouteEmptyTurnsNeverCallsSegmenter(t *testing.T) {
	seg := &fakeSegmenter{}
	store := newFakeThreadStore()
	at := mustAutoThreader(t, seg, store)
	for _, turns := range [][]Turn{nil, {}} {
		threadIDs, err := at.Route(context.Background(), turns)
		if err != nil {
			t.Fatalf("Route(%v): %v", turns, err)
		}
		if threadIDs != nil {
			t.Fatalf("Route(%v) = %v, want nil", turns, threadIDs)
		}
	}
	if seg.calls != 0 {
		t.Fatalf("Segment called %d times for an empty window, want 0: the short circuit must precede the Segmenter", seg.calls)
	}
	if store.threadSeq != 0 {
		t.Fatalf("Route over an empty window created %d threads, want 0", store.threadSeq)
	}
}

// TestAutoThreaderRouteTwiceIsIdempotent is the re-delivery rule at the
// Route level: the same window delivered twice files N turns, not 2N,
// because each turn's id is its content address at its window index.
func TestAutoThreaderRouteTwiceIsIdempotent(t *testing.T) {
	store := newFakeThreadStore()
	at := mustAutoThreader(t, &fakeSegmenter{boundaries: []Boundary{{TurnIndex: 2, Label: "code"}}}, store)
	turns := []Turn{
		{Speaker: "a", Text: "zero"}, {Speaker: "b", Text: "one"},
		{Speaker: "a", Text: "two"}, {Speaker: "b", Text: "three"},
	}
	ctx := context.Background()
	first, err := at.Route(ctx, turns)
	if err != nil {
		t.Fatalf("Route (first): %v", err)
	}
	second, err := at.Route(ctx, turns)
	if err != nil {
		t.Fatalf("Route (second): %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("Route returned %d thread ids then %d, want the same partition both times", len(first), len(second))
	}
	total := 0
	for _, id := range second {
		total += len(store.texts(id))
	}
	if total != len(turns) {
		t.Fatalf("after routing the same %d-turn window twice the store holds %d turns, want %d",
			len(turns), total, len(turns))
	}
}

// TestAutoThreaderRouteRefusesBadBoundaries is buildSegments' hardening:
// every boundary index that cannot describe a partition of the window is a
// typed KindInvalidInput error naming the index, never a panic on the slice
// expression and never an empty segment that would create a thread no turn
// was filed under.
func TestAutoThreaderRouteRefusesBadBoundaries(t *testing.T) {
	turns := []Turn{{Speaker: "a", Text: "zero"}, {Speaker: "b", Text: "one"}}
	for name, boundaries := range map[string][]Boundary{
		"negative":   {{TurnIndex: -1, Label: "code"}},
		"zero":       {{TurnIndex: 0, Label: "code"}},
		"at end":     {{TurnIndex: 2, Label: "code"}},
		"past end":   {{TurnIndex: 7, Label: "code"}},
		"duplicated": {{TurnIndex: 1, Label: "code"}, {TurnIndex: 1, Label: "code"}},
		"decreasing": {{TurnIndex: 1, Label: "code"}, {TurnIndex: 0, Label: "code"}},
	} {
		store := newFakeThreadStore()
		at := mustAutoThreader(t, &fakeSegmenter{boundaries: boundaries}, store)
		got, err := at.Route(context.Background(), turns)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Route with a %s boundary index: error = %v, want KindInvalidInput", name, err)
		}
		if got != nil {
			t.Fatalf("Route with a %s boundary index returned %v, want no thread ids", name, got)
		}
		if store.threadSeq != 0 {
			t.Fatalf("Route with a %s boundary index created %d threads, want 0: a zero-turn segment must never create one",
				name, store.threadSeq)
		}
	}
}

// TestAutoThreaderRouteRefusesUnusableTopicType is the key-space boundary:
// a taxonomy whose resolved TopicType could not be a safe storage key is
// refused before anything is filed, rather than silently rewritten.
func TestAutoThreaderRouteRefusesUnusableTopicType(t *testing.T) {
	store := newFakeThreadStore()
	at, err := NewAutoThreader(&fakeSegmenter{}, store, NewTaxonomyConfig(nil, TopicType("")),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	if _, err := at.Route(context.Background(), []Turn{{Speaker: "a", Text: "x"}}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Route with an empty resolved TopicType: error = %v, want KindInvalidInput", err)
	}
	if store.threadSeq != 0 {
		t.Fatalf("Route created %d threads for an unusable TopicType, want 0", store.threadSeq)
	}
}
