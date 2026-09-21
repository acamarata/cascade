// Package topics (reassign_test.go): Purpose: AutoThreader.Reassign's
// move+exemplar+publish sequence, the published MisfileEvent's decoded
// payload, the no-op refusal, and every dependency failure's
// propagation-and-short-circuit behavior (a failed step must never let a
// later step run). fakeThreadStore comes from thread_store_test.go;
// fixedClock and mustExemplarStore from exemplar_store_test.go;
// mustAutoThreader from auto_thread_test.go; refusingStore and fakePublisher
// from reassign_doubles_test.go. Injected failures are plain errors.New
// values for the reason auto_thread_errors_test.go's header states:
// errors.Is against a cascade sentinel compares Kind only.
package topics

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestReassigner returns an AutoThreader wired over store and pub plus
// the ExemplarStore it will write to, so a test can assert on the exemplar
// side without re-deriving it.
func newTestReassigner(t *testing.T, store ThreadStore, pub MisfileEventPublisher) (*AutoThreader, *ExemplarStore) {
	t.Helper()
	es := mustExemplarStore(t, storetest.NewMemStore())
	at, err := NewAutoThreader(&fakeSegmenter{}, store, testTaxonomy(), es, pub, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	return at, es
}

// TestReassignMovesAddsExemplarAndPublishes asserts the whole sequence AND
// decodes the published payload field by field: a MisfileEvent whose fields
// were all left at their zero values would satisfy "an event was published"
// while saying nothing true about what moved, so every field is checked
// against its expected non-zero value here.
func TestReassignMovesAddsExemplarAndPublishes(t *testing.T) {
	store := newFakeThreadStore()
	pub := &fakePublisher{}
	at, es := newTestReassigner(t, store, pub)
	turn := Turn{Speaker: "a", Text: "misfiled turn"}

	err := at.Reassign(context.Background(), ThreadID("t1"), TurnID("u1"), turn, TopicType("general"), TopicType("code"))
	if err != nil {
		t.Fatalf("Reassign: %v", err)
	}

	if len(store.moves) != 1 || store.moves[0] != (moveCall{threadID: "t1", turnID: "u1", newType: "code"}) {
		t.Fatalf("moves = %+v, want exactly one MoveTurn(t1, u1, code)", store.moves)
	}

	exemplars, err := es.Exemplars(context.Background(), TopicType("code"))
	if err != nil {
		t.Fatalf("Exemplars: %v", err)
	}
	if len(exemplars) != 1 || exemplars[0] != turn {
		t.Fatalf("Exemplars(code) = %+v, want [%+v]", exemplars, turn)
	}

	assertOneMisfileEvent(t, pub, MisfileEvent{
		ThreadID: ThreadID("t1"), TurnID: TurnID("u1"),
		FromTopicType: TopicType("general"), ToTopicType: TopicType("code"),
		ReassignedAt: newFixedClock().Now().Unix(),
	})
}

// assertOneMisfileEvent checks that pub saw exactly one publish, on the
// right kind/namespace/source, whose DECODED payload equals want field for
// field. Decoding matters: asserting only that "an event was published"
// would pass for a MisfileEvent with every field left at its zero value,
// which says nothing true about what moved.
func assertOneMisfileEvent(t *testing.T, pub *fakePublisher, want MisfileEvent) {
	t.Helper()
	if len(pub.calls) != 1 {
		t.Fatalf("Publish called %d times, want 1", len(pub.calls))
	}
	call := pub.calls[0]
	if call.kind != EventKindMisfileReassigned {
		t.Fatalf("published kind = %q, want %q", call.kind, EventKindMisfileReassigned)
	}
	if call.namespace != misfileNamespace {
		t.Fatalf("published namespace = %q, want the audit domain %q", call.namespace, misfileNamespace)
	}
	if call.source != misfileEventSource {
		t.Fatalf("published source = %q, want %q", call.source, misfileEventSource)
	}
	var got MisfileEvent
	if err := json.Unmarshal(call.payload, &got); err != nil {
		t.Fatalf("decoding the published payload: %v", err)
	}
	if got != want {
		t.Fatalf("published MisfileEvent = %+v, want %+v", got, want)
	}
	if got.ReassignedAt == 0 {
		t.Fatalf("published MisfileEvent carries a zero timestamp: %+v", got)
	}
}

// TestReassignNoOpReturnsTypedErrorWithoutSideEffects is the acceptance
// criterion's own case: threadID/newTopicType already match current
// placement. Nothing downstream may run - proven here by asserting zero
// moves, zero exemplars, and zero publishes, not just a non-nil error.
func TestReassignNoOpReturnsTypedErrorWithoutSideEffects(t *testing.T) {
	store := newFakeThreadStore()
	pub := &fakePublisher{}
	at, es := newTestReassigner(t, store, pub)

	err := at.Reassign(context.Background(), ThreadID("t1"), TurnID("u1"), Turn{Speaker: "a", Text: "x"},
		TopicType("code"), TopicType("code"))
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("no-op Reassign error = %v, want KindConflict", err)
	}
	if len(store.moves) != 0 {
		t.Fatalf("moves = %+v, want none: a no-op must never call MoveTurn", store.moves)
	}
	exemplars, exErr := es.Exemplars(context.Background(), TopicType("code"))
	if exErr != nil {
		t.Fatalf("Exemplars: %v", exErr)
	}
	if len(exemplars) != 0 {
		t.Fatalf("Exemplars(code) = %+v, want none: a no-op must never add an exemplar", exemplars)
	}
	if len(pub.calls) != 0 {
		t.Fatalf("Publish called %d times, want 0: a no-op must never publish", len(pub.calls))
	}
}

func TestReassignRefusesUnusableTargetTopic(t *testing.T) {
	store := newFakeThreadStore()
	at, _ := newTestReassigner(t, store, &fakePublisher{})
	err := at.Reassign(context.Background(), ThreadID("t1"), TurnID("u1"), Turn{Speaker: "a", Text: "x"},
		TopicType("general"), TopicType("bad topic"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Reassign into an unusable TopicType: error = %v, want KindInvalidInput", err)
	}
	if len(store.moves) != 0 {
		t.Fatalf("moves = %+v, want none: an unusable target topic must be refused before the move", store.moves)
	}
}

func TestReassignMoveTurnErrorPropagatesAndStopsSequence(t *testing.T) {
	store := newFakeThreadStore()
	wantErr := errors.New("store down")
	store.moveErr = wantErr
	pub := &fakePublisher{}
	at, es := newTestReassigner(t, store, pub)

	err := at.Reassign(context.Background(), ThreadID("t1"), TurnID("u1"), Turn{Speaker: "a", Text: "x"},
		TopicType("general"), TopicType("code"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Reassign error = %v, want the injected MoveTurn error %v", err, wantErr)
	}
	exemplars, _ := es.Exemplars(context.Background(), TopicType("code"))
	if len(exemplars) != 0 {
		t.Fatalf("Exemplars(code) = %+v, want none: a failed MoveTurn must never add an exemplar", exemplars)
	}
	if len(pub.calls) != 0 {
		t.Fatalf("Publish called %d times, want 0 after a failed MoveTurn", len(pub.calls))
	}
}

func TestReassignExemplarAddErrorPropagatesAndSkipsPublish(t *testing.T) {
	store := newFakeThreadStore()
	pub := &fakePublisher{}
	// A negative maxDepth is impossible through NewExemplarStore's own
	// validation, so to force Add to fail this test uses a Store double
	// that refuses every write instead. The assertion is on the refusal's
	// own message, not errors.Is: the error is constructed fresh inside
	// refusingStore, so there is no identity to compare against, and a
	// Kind-only match would pass for any KindUnavailable error.
	at, err := NewAutoThreader(&fakeSegmenter{}, store, testTaxonomy(),
		mustExemplarStore(t, refusingStore{}), pub, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	err = at.Reassign(context.Background(), ThreadID("t1"), TurnID("u1"), Turn{Speaker: "a", Text: "x"},
		TopicType("general"), TopicType("code"))
	if err == nil || !strings.Contains(err.Error(), "put refused") {
		t.Fatalf("Reassign with a refusing ExemplarStore backend: error = %v, want the propagated \"put refused\" failure", err)
	}
	if len(pub.calls) != 0 {
		t.Fatalf("Publish called %d times, want 0 after a failed Add", len(pub.calls))
	}
	if len(store.moves) != 1 {
		t.Fatalf("moves = %+v, want exactly one: MoveTurn must still have run before Add failed", store.moves)
	}
}

func TestReassignPublishErrorPropagates(t *testing.T) {
	store := newFakeThreadStore()
	wantErr := errors.New("bus down")
	pub := &fakePublisher{err: wantErr}
	at, _ := newTestReassigner(t, store, pub)

	err := at.Reassign(context.Background(), ThreadID("t1"), TurnID("u1"), Turn{Speaker: "a", Text: "x"},
		TopicType("general"), TopicType("code"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Reassign error = %v, want the injected publish error %v", err, wantErr)
	}
	if len(store.moves) != 1 {
		t.Fatalf("moves = %+v, want exactly one: MoveTurn and Add must still have run before publish failed", store.moves)
	}
}

func TestReassignRejectsNilContext(t *testing.T) {
	store := newFakeThreadStore()
	at, _ := newTestReassigner(t, store, &fakePublisher{})
	//nolint:staticcheck // deliberate nil ctx to prove the guard fires
	err := at.Reassign(nil, ThreadID("t1"), TurnID("u1"), Turn{}, TopicType("a"), TopicType("b"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Reassign(nil ctx, ...) error = %v, want KindInvalidInput", err)
	}
}

// TestTopicsPlatformParity is Art.5's explicit per-platform assertion for
// this ticket's sub-systems, the same posture TestSegmenterPlatformParity
// (segmenter_core_test.go, P1-E21-W5-S45-T2) already establishes for this
// package: pure Go, no CGO, no OS-specific path or platform-conditional
// branch anywhere in thread_store.go, taxonomy.go, auto_thread.go,
// exemplar_store.go, or reassign.go, so the CI matrix (macOS, Linux,
// Windows) running this SAME named test to a PASS on all three is the
// explicit per-platform result Art.5 requires. The assertion drives the
// real, deterministic pipeline end to end (Route then Reassign against a
// real storetest.MemStore-backed ExemplarStore) rather than asserting
// something platform-independent by construction alone.
func TestTopicsPlatformParity(t *testing.T) {
	t.Logf("topics auto-thread/taxonomy/exemplar/reassign platform parity: GOOS=%s GOARCH=%s", runtime.GOOS, runtime.GOARCH)

	store := newFakeThreadStore()
	pub := &fakePublisher{}
	es := mustExemplarStore(t, storetest.NewMemStore())
	at, err := NewAutoThreader(&fakeSegmenter{boundaries: []Boundary{{TurnIndex: 1, Label: "code"}}},
		store, testTaxonomy(), es, pub, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	turns := []Turn{{Speaker: "a", Text: "general question"}, {Speaker: "a", Text: "some code"}}
	threadIDs, err := at.Route(context.Background(), turns)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(threadIDs) != 2 {
		t.Fatalf("platform parity on %s/%s: Route returned %d thread ids, want 2", runtime.GOOS, runtime.GOARCH, len(threadIDs))
	}

	misfiledTurn := Turn{Speaker: "a", Text: "general question"}
	if err := at.Reassign(context.Background(), threadIDs[0], TurnID("u0"), misfiledTurn,
		TopicType("fallback"), TopicType("code-topic")); err != nil {
		t.Fatalf("Reassign: %v", err)
	}
	exemplars, err := es.Exemplars(context.Background(), TopicType("code-topic"))
	if err != nil {
		t.Fatalf("Exemplars: %v", err)
	}
	if len(exemplars) != 1 || exemplars[0] != misfiledTurn {
		t.Fatalf("platform parity on %s/%s: Exemplars(code-topic) = %+v, want [%+v]",
			runtime.GOOS, runtime.GOARCH, exemplars, misfiledTurn)
	}
}
