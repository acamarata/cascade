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

func TestNewAutoThreaderRejectsNilDependencies(t *testing.T) {
	store := newFakeThreadStore()
	seg := &fakeSegmenter{}
	es := mustExemplarStore(t, storetest.NewMemStore())
	pub := &fakePublisher{}
	clock := newFixedClock()
	for name, err := range map[string]error{
		"nil segmenter": errFromNewAutoThreader(nil, store, es, pub, clock),
		"nil store":     errFromNewAutoThreader(seg, nil, es, pub, clock),
		"nil exemplars": errFromNewAutoThreader(seg, store, nil, pub, clock),
		"nil publisher": errFromNewAutoThreader(seg, store, es, nil, clock),
		"nil clock":     errFromNewAutoThreader(seg, store, es, pub, nil),
	} {
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("NewAutoThreader with a %s: error = %v, want KindInvalidInput", name, err)
		}
	}
}

// errFromNewAutoThreader is the table above's one-line call form; it exists
// only so each row reads as the dependency it nils out.
func errFromNewAutoThreader(
	seg Segmenter, store ThreadStore, es *ExemplarStore, pub MisfileEventPublisher, clock Clock,
) error {
	_, err := NewAutoThreader(seg, store, testTaxonomy(), es, pub, clock)
	return err
}

// TestNewDefaultAutoThreaderWiresRealSegmenter proves NewDefaultAutoThreader
// really calls NewSegmenter (segmenter_core.go) rather than something that
// only compiles against its signature: fakeClassifyExecutor records every
// dispatch it receives, so a non-empty requests slice after Route can only
// mean the real classify-lane pipeline ran.
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
		t.Fatalf("classify executor received %d requests, want 1 - NewDefaultAutoThreader did not wire a working NewSegmenter", len(exec.requests))
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
