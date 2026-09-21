// Package topics (observe_log_test.go): ObserveLogger's construction and
// entry guards, every field of the audit event observe mode publishes, and
// every failure observe mode can meet. The mode-branching table is in
// observe_mode_test.go, the first-use window's persistence and storage
// coordinates in observe_state_test.go, the observe-vs-Route agreement pin in
// observe_pin_test.go, and the shared helpers and store doubles in
// observe_doubles_test.go. Other doubles come from this package's siblings:
// fakeSegmenter and mustAutoThreader (auto_thread_test.go), fakeThreadStore
// (thread_store_test.go), fixedClock/mustExemplarStore (exemplar_store_test.go),
// fakePublisher (reassign_doubles_test.go).
//
// PROPAGATION TESTS INJECT PLAIN errors.New VALUES for the reason
// auto_thread_errors_test.go's header states: cascade.Error's Is hook
// compares Kind only, so errors.Is against a cascade sentinel would pass for
// any error of that Kind and prove nothing about which error came back.
package topics

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestNewObserveLoggerRefusesNilDependencies(t *testing.T) {
	at := mustAutoThreader(t, &fakeSegmenter{}, newFakeThreadStore())
	store, clock, pub := storetest.NewMemStore(), newFixedClock(), &fakePublisher{}
	cases := []struct {
		name     string
		threader *AutoThreader
		store    provider.Store
		clock    Clock
		audit    AuditPublisher
	}{
		{"nil threader", nil, store, clock, pub},
		{"nil store", at, nil, clock, pub},
		{"nil clock", at, store, nil, pub},
		{"nil audit", at, store, clock, nil},
	}
	for _, c := range cases {
		_, err := NewObserveLogger(c.threader, c.store, c.clock, c.audit)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("NewObserveLogger(%s) error = %v, want KindInvalidInput", c.name, err)
		}
	}
}

func TestObserveLoggerObserveGuards(t *testing.T) {
	at := mustAutoThreader(t, &fakeSegmenter{}, newFakeThreadStore())
	t.Run("nil ctx", func(t *testing.T) {
		ol := mustObserveLogger(t, at, storetest.NewMemStore(), newFixedClock(), &fakePublisher{})
		if _, err := ol.Observe(nil, observeTurns()); !cascade.HasKind(err, cascade.KindInvalidInput) { //nolint:staticcheck
			t.Fatalf("Observe(nil ctx) error = %v, want KindInvalidInput", err)
		}
	})
	t.Run("empty turns is a no-op before any store touch", func(t *testing.T) {
		store := &onceStore{Store: storetest.NewMemStore(), limit: 0}
		ol := mustObserveLogger(t, at, store, newFixedClock(), &fakePublisher{})
		got, err := ol.Observe(context.Background(), nil)
		if err != nil {
			t.Fatalf("Observe(nil turns): %v, want nil error", err)
		}
		wantEq(t, got.Observed, false, "ObserveResult.Observed for an empty window")
		if got.ThreadIDs != nil || got.Proposals != nil {
			t.Fatalf("Observe(nil turns) = %+v, want a zero ObserveResult", got)
		}
	})
}

// TestObserveLoggerElapsedZeroObservesAndEmitsEvent decodes the event
// published on a first-use call field by field. The proposals are
// would_create markers here because no thread exists yet on a fresh store -
// observe_pin_test.go covers the existing-thread case.
func TestObserveLoggerElapsedZeroObservesAndEmitsEvent(t *testing.T) {
	clock, fts, pub := newFixedClock(), newFakeThreadStore(), &fakePublisher{}
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), fts), storetest.NewMemStore(), clock, pub)
	got, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	wantEq(t, got.Observed, true, "ObserveResult.Observed in observe mode")
	if got.ThreadIDs != nil {
		t.Fatalf("Observe (observe mode) ThreadIDs = %v, want nil (no thread was created)", got.ThreadIDs)
	}
	wantProposals := []Proposal{
		{TopicType: TopicType("fallback"), ThreadID: ThreadID("would_create:fallback")},
		{TopicType: TopicType("code-topic"), ThreadID: ThreadID("would_create:code-topic")},
	}
	if !slices.Equal(got.Proposals, wantProposals) {
		t.Fatalf("Proposals = %+v, want %+v", got.Proposals, wantProposals)
	}
	observing, err := ol.IsObserving()
	if err != nil {
		t.Fatalf("IsObserving: %v", err)
	}
	wantEq(t, observing, true, "IsObserving() at elapsed 0h")
	wantEq(t, fts.threadSeq, 0, "threadSeq (Route must never be called in observe mode)")
	assertObserveEventFields(t, pub)
}

// assertObserveEventFields checks the one published event's coordinates and
// payload against literal expected values, split out of the test above to
// keep both functions well under the 50-line cap.
func assertObserveEventFields(t *testing.T, pub *fakePublisher) {
	t.Helper()
	wantEq(t, len(pub.calls), 1, "audit events published")
	wantEq(t, pub.calls[0].namespace, "audit", "published namespace (literal storage.DomainAudit)")
	wantEq(t, pub.calls[0].kind, EventKindTopicObserve, "published kind")
	wantEq(t, pub.calls[0].source, "internal/conversation/topics", "published source")
	evt := decodeObserveEvent(t, pub.calls[0].payload)
	wantEq(t, evt.Event, "topic_observe", "Event field (literal contract value)")
	wantBoundaries := []proposedBoundary{{TurnIndex: 1, TopicType: TopicType("code-topic")}}
	if !slices.Equal(evt.ProposedBoundaries, wantBoundaries) {
		t.Fatalf("ProposedBoundaries = %+v, want %+v", evt.ProposedBoundaries, wantBoundaries)
	}
	wantIDs := []ThreadID{"would_create:fallback", "would_create:code-topic"}
	if !slices.Equal(evt.ProposedThreadIDs, wantIDs) {
		t.Fatalf("ProposedThreadIDs = %v, want %v", evt.ProposedThreadIDs, wantIDs)
	}
	wantEq(t, evt.ElapsedDays, 0.0, "ElapsedDays at first Observe call")
}

// TestObserveLoggerProposedBoundariesPairEachWithItsOwnLabel: with TWO
// boundaries whose labels resolve to different topics, ProposedBoundaries
// must pair each entry's TurnIndex with THAT boundary's own resolved
// TopicType, in boundary order. Every other fixture in this package emits
// at most one boundary, which cannot distinguish "each boundary's own
// label" from any other per-index pairing (e.g. a reversed one).
func TestObserveLoggerProposedBoundariesPairEachWithItsOwnLabel(t *testing.T) {
	clock, pub := newFixedClock(), &fakePublisher{}
	seg := &fakeSegmenter{boundaries: []Boundary{
		{TurnIndex: 1, Label: "code"},
		{TurnIndex: 2, Label: "unmapped"},
	}}
	turns := []Turn{
		{Speaker: "a", Text: "one"}, {Speaker: "a", Text: "two"}, {Speaker: "a", Text: "three"},
	}
	ol := mustObserveLogger(t, mustAutoThreader(t, seg, newFakeThreadStore()), storetest.NewMemStore(), clock, pub)
	if _, err := ol.Observe(context.Background(), turns); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	wantEq(t, len(pub.calls), 1, "audit events published")
	evt := decodeObserveEvent(t, pub.calls[0].payload)
	wantBoundaries := []proposedBoundary{
		{TurnIndex: 1, TopicType: TopicType("code-topic")},
		{TurnIndex: 2, TopicType: TopicType("fallback")},
	}
	if !slices.Equal(evt.ProposedBoundaries, wantBoundaries) {
		t.Fatalf("ProposedBoundaries = %+v, want %+v (each turn_index paired with its OWN label)",
			evt.ProposedBoundaries, wantBoundaries)
	}
}

// TestObserveLoggerEventTimestampsAtNonZeroElapsed is the assertion the
// zero-elapsed test cannot make: at elapsed 0 a wrong divisor and a wrong
// timestamp source still produce the same bytes, so observed_at and
// elapsed_days are pinned here at a 72h elapsed, where first_use_at, raw
// hours and a non-RFC3339 layout are each a different value (CR finding 2).
func TestObserveLoggerEventTimestampsAtNonZeroElapsed(t *testing.T) {
	clock, pub := newFixedClock(), &fakePublisher{}
	store := storetest.NewMemStore()
	firstUseAt := clock.now.Add(-72 * time.Hour)
	seedFirstUseAt(t, store, firstUseAt)
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), newFakeThreadStore()), store, clock, pub)
	if _, err := ol.Observe(context.Background(), observeTurns()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	wantEq(t, len(pub.calls), 1, "audit events published")
	evt := decodeObserveEvent(t, pub.calls[0].payload)
	wantEq(t, evt.ElapsedDays, 3.0, "ElapsedDays at a 72h elapsed (hours/24, not raw hours)")
	wantEq(t, evt.ObservedAt, clock.now.UTC().Format(time.RFC3339), "ObservedAt (the clock's now, RFC3339)")
	if evt.ObservedAt == firstUseAt.UTC().Format(time.RFC3339) {
		t.Fatalf("ObservedAt = %q, which is first_use_at, not the observation time", evt.ObservedAt)
	}
	parsed, err := time.Parse(time.RFC3339, evt.ObservedAt)
	if err != nil {
		t.Fatalf("ObservedAt %q does not parse as RFC3339: %v", evt.ObservedAt, err)
	}
	if !parsed.Equal(clock.now) {
		t.Fatalf("ObservedAt parsed to %v, want the clock's now %v", parsed, clock.now)
	}
}

// TestObserveLoggerPublishErrorPropagates: the event is observe mode's only
// product, so a failed publish is a failed Observe (CR finding 3).
func TestObserveLoggerPublishErrorPropagates(t *testing.T) {
	wantErr := errors.New("bus down")
	pub := &fakePublisher{err: wantErr}
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), newFakeThreadStore()),
		storetest.NewMemStore(), newFixedClock(), pub)
	got, err := ol.Observe(context.Background(), observeTurns())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Observe error = %v, want the injected publish error %v", err, wantErr)
	}
	wantEq(t, got.Observed, false, "ObserveResult.Observed after a failed publish")
	wantEq(t, len(pub.calls), 1, "publish attempts")
}

// TestObserveLoggerObserveModeErrorPaths: every failure observe mode can
// meet is returned, and none of them publishes an event.
func TestObserveLoggerObserveModeErrorPaths(t *testing.T) {
	segErr, lookupErr := errors.New("segmenter down"), errors.New("thread store down")
	cases := []struct {
		name     string
		threader func(t *testing.T) *AutoThreader
		wantIs   error
		wantKind cascade.Kind
	}{
		{"segmenter failure", func(t *testing.T) *AutoThreader {
			return mustAutoThreader(t, &fakeSegmenter{err: segErr}, newFakeThreadStore())
		}, segErr, 0},
		{"boundary list that does not partition the window", func(t *testing.T) *AutoThreader {
			return mustAutoThreader(t,
				&fakeSegmenter{boundaries: []Boundary{{TurnIndex: 0, Label: "code"}}}, newFakeThreadStore())
		}, nil, cascade.KindInvalidInput},
		{"unusable resolved TopicType", func(t *testing.T) *AutoThreader {
			return mustEmptyTaxonomyThreader(t)
		}, nil, cascade.KindInvalidInput},
		{"thread store lookup failure", func(t *testing.T) *AutoThreader {
			fts := newFakeThreadStore()
			fts.lookupErr = lookupErr
			return mustAutoThreader(t, codeBoundarySegmenter(), fts)
		}, lookupErr, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub := &fakePublisher{}
			ol := mustObserveLogger(t, c.threader(t), storetest.NewMemStore(), newFixedClock(), pub)
			got, err := ol.Observe(context.Background(), observeTurns())
			if c.wantIs != nil && !errors.Is(err, c.wantIs) {
				t.Fatalf("Observe error = %v, want the injected %v", err, c.wantIs)
			}
			if c.wantIs == nil && !cascade.HasKind(err, c.wantKind) {
				t.Fatalf("Observe error = %v, want kind %v", err, c.wantKind)
			}
			wantEq(t, got.Observed, false, "ObserveResult.Observed after a failure")
			wantEq(t, len(pub.calls), 0, "audit events published after a failure")
		})
	}
}

// mustEmptyTaxonomyThreader wires an AutoThreader whose taxonomy resolves
// every label to the empty TopicType, which validateTopicType refuses.
func mustEmptyTaxonomyThreader(t *testing.T) *AutoThreader {
	t.Helper()
	at, err := NewAutoThreader(codeBoundarySegmenter(), &fakeClassifier{}, newFakeThreadStore(), NewTaxonomyConfig(nil, TopicType("")),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	return at
}
