// Package topics (auto_thread_errors_test.go): Purpose: AutoThreader's
// error paths - every dependency failure Route propagates without
// swallowing, the constructor's nil-dependency refusals, and
// NewDefaultAutoThreader's real-NewSegmenter wiring. Split from
// auto_thread_test.go only to keep both files under the 300-line cap
// (Art.10.3); the doubles and mustAutoThreader helper live there.
//
// PROPAGATION TESTS INJECT PLAIN errors.New VALUES, never cascade errors:
// cascade.Error's own Is hook compares Kind only, so errors.Is against a
// cascade sentinel would pass for any error of the same Kind and would
// prove nothing about which error actually came back. A plain error makes
// errors.Is an identity check again.
package topics

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAutoThreaderRouteSegmenterErrorPropagates(t *testing.T) {
	wantErr := errors.New("segmenter down")
	seg := &fakeSegmenter{err: wantErr}
	store := newFakeThreadStore()
	at := mustAutoThreader(t, seg, store)
	_, err := at.Route(context.Background(), []Turn{{Speaker: "a", Text: "x"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Route error = %v, want the injected segmenter error %v", err, wantErr)
	}
	if store.threadSeq != 0 {
		t.Fatalf("Route created %d threads after a segmenter failure, want 0 (never reached the store)", store.threadSeq)
	}
}

func TestAutoThreaderRouteThreadStoreCreateOrSelectErrorPropagates(t *testing.T) {
	store := newFakeThreadStore()
	wantErr := errors.New("store down")
	store.createErr = wantErr
	at := mustAutoThreader(t, &fakeSegmenter{}, store)
	_, err := at.Route(context.Background(), []Turn{{Speaker: "a", Text: "x"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Route error = %v, want the injected CreateOrSelect error %v", err, wantErr)
	}
}

func TestAutoThreaderRouteThreadStoreAppendTurnErrorPropagates(t *testing.T) {
	store := newFakeThreadStore()
	wantErr := errors.New("append down")
	store.appendErr = wantErr
	at := mustAutoThreader(t, &fakeSegmenter{}, store)
	_, err := at.Route(context.Background(), []Turn{{Speaker: "a", Text: "x"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Route error = %v, want the injected AppendTurn error %v", err, wantErr)
	}
	if store.threadSeq != 1 {
		t.Fatalf("threadSeq = %d, want CreateOrSelect to have succeeded once before AppendTurn failed", store.threadSeq)
	}
}

// TestAutoThreaderRouteClassifierErrorPropagates covers the Classifier's
// own error path (P1-E21-W5-S46-T5 D5): a dispatch failure classifying the
// window's opener must propagate through Route exactly like a Segmenter or
// ThreadStore failure, never swallowed into a fallback-topic guess.
func TestAutoThreaderRouteClassifierErrorPropagates(t *testing.T) {
	wantErr := errors.New("classifier down")
	store := newFakeThreadStore()
	at, err := NewAutoThreader(&fakeSegmenter{}, &fakeClassifier{err: wantErr}, store, testTaxonomy(),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	_, routeErr := at.Route(context.Background(), []Turn{{Speaker: "a", Text: "x"}})
	if !errors.Is(routeErr, wantErr) {
		t.Fatalf("Route error = %v, want the injected classifier error %v", routeErr, wantErr)
	}
	if store.threadSeq != 0 {
		t.Fatalf("Route created %d threads after a classifier failure, want 0 (never reached the store)", store.threadSeq)
	}
}

func TestNewAutoThreaderRejectsNilDependencies(t *testing.T) {
	store := newFakeThreadStore()
	seg := &fakeSegmenter{}
	cl := &fakeClassifier{}
	es := mustExemplarStore(t, storetest.NewMemStore())
	pub := &fakePublisher{}
	clock := newFixedClock()
	for name, err := range map[string]error{
		"nil segmenter":  errFromNewAutoThreader(nil, cl, store, es, pub, clock),
		"nil classifier": errFromNewAutoThreader(seg, nil, store, es, pub, clock),
		"nil store":      errFromNewAutoThreader(seg, cl, nil, es, pub, clock),
		"nil exemplars":  errFromNewAutoThreader(seg, cl, store, nil, pub, clock),
		"nil publisher":  errFromNewAutoThreader(seg, cl, store, es, nil, clock),
		"nil clock":      errFromNewAutoThreader(seg, cl, store, es, pub, nil),
	} {
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("NewAutoThreader with a %s: error = %v, want KindInvalidInput", name, err)
		}
	}
}

// errFromNewAutoThreader is the table above's one-line call form; it exists
// only so each row reads as the dependency it nils out.
func errFromNewAutoThreader(
	seg Segmenter, cl Classifier, store ThreadStore, es *ExemplarStore, pub MisfileEventPublisher, clock Clock,
) error {
	_, err := NewAutoThreader(seg, cl, store, testTaxonomy(), es, pub, clock)
	return err
}

// TestNewDefaultAutoThreaderWiresRealSegmenter proves NewDefaultAutoThreader
// really calls NewSegmenterWith (segmenter_core.go) rather than something
// that only compiles against its signature: fakeClassifyExecutor records
// every dispatch it receives, so a non-empty requests slice after Route can
// only mean the real classify-lane pipeline ran. UPDATED for
// P1-E21-W5-S46-T5 D6 (2026-09-21, scope deviation - see the ticket's
// narrow-fix report): this test previously asserted 2 requests for a
// 1-turn window, proving the very double-dispatch defect D6 exists to
// remove (segmenter_core.go's classifyAll and auto_thread.go's
// classifyOpener each built their own NewClassifier over the same
// executor). NewDefaultAutoThreader now builds exactly ONE Classifier and
// shares it, and cheapLaneClassifier memoizes by turn text
// (segmenter_types.go), so the opener's second Classify call for the
// identical turn-0 text is a cache hit: exactly 1 request, not 2. The
// full counting-executor proof (dispatch count plus the filed opener label
// matching the segmenter's) is TestNewDefaultAutoThreaderClassifiesOpenerOnce
// in auto_thread_opener_test.go.
func TestNewDefaultAutoThreaderWiresRealSegmenter(t *testing.T) {
	exec := &fakeClassifyExecutor{labels: []string{"code"}}
	emb := &fakeEmbedder{vectors: [][]float32{{1, 0}}}
	store := newFakeThreadStore()
	at, err := NewDefaultAutoThreader(exec, emb, validCfg(), store, testTaxonomy(),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewDefaultAutoThreader: %v", err)
	}
	if _, err := at.Route(context.Background(), turnsN(1)); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(exec.requests) != 1 {
		t.Fatalf("classify executor received %d requests, want 1 (Segment's classifyAll dispatches once; "+
			"the opener classification for the SAME turn-0 text is a memo cache hit, not a second dispatch "+
			"- P1-E21-W5-S46-T5 D6) - NewDefaultAutoThreader did not wire a shared NewSegmenterWith/NewClassifier",
			len(exec.requests))
	}
}

func TestNewDefaultAutoThreaderPropagatesSegmenterConstructionError(t *testing.T) {
	store := newFakeThreadStore()
	_, err := NewDefaultAutoThreader(nil, &fakeEmbedder{}, validCfg(), store, testTaxonomy(),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewDefaultAutoThreader(nil executor, ...) error = %v, want KindInvalidInput (from NewSegmenter)", err)
	}
}
