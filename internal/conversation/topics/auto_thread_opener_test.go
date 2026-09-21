// Package topics (auto_thread_opener_test.go): Purpose: the P1-E21-W5-
// S46-T5 D5 fix proof - AutoThreader.plan's classifyOpener (auto_thread.go)
// labels the window's implicit opening segment with the Classifier's real
// classification of its first turn, instead of leaving it "" (which
// TaxonomyConfig.Resolve maps to Fallback regardless of the opener's real
// content, the Epic U defect this acceptance ticket surfaced); and the D6
// fix proof - NewDefaultAutoThreader shares ONE Classifier (segmenter_core.
// go's NewSegmenterWith / segmenter_types.go's memo cache) between
// segmenterImpl.classifyAll and classifyOpener, so turn 0 dispatches once
// per Route window, never twice with a chance to diverge. Uses
// fakeSegmenter and fakeClassifier (auto_thread_test.go), testTaxonomy and
// mustAutoThreader (auto_thread_test.go), fakeClassifyExecutor and
// fakeEmbedder (segmenter_core_test.go), newFakeThreadStore
// (thread_store_test.go), all in this same package.
// SPORT: internal/conversation/topics auto-thread (ADD) (P1-E21-W5-S45-T3);
//
//	fix per P1-E21-W5-S46-T5 D5; shared-classifier fix per D6.
package topics

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestAutoThreaderClassifiesOpenerWithSignal is D5's first required proof:
// an opener whose text carries a clear topic files under that topic, not
// the fallback. fakeSegmenter reports no boundaries, so the whole window is
// the implicit opening segment; fakeClassifier returns "code", which
// testTaxonomy maps to "code-topic" - distinct from its "fallback" value,
// so a thread under "fallback" would prove the pre-fix defect is still
// present.
func TestAutoThreaderClassifiesOpenerWithSignal(t *testing.T) {
	seg := &fakeSegmenter{}
	classifier := &fakeClassifier{label: "code"}
	store := newFakeThreadStore()
	at, err := NewAutoThreader(seg, classifier, store, testTaxonomy(),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	turns := []Turn{{Speaker: "a", Text: "let's talk about code review"}}
	threadIDs, err := at.Route(context.Background(), turns)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(threadIDs) != 1 {
		t.Fatalf("Route returned %d thread ids, want 1", len(threadIDs))
	}
	if classifier.calls != 1 {
		t.Fatalf("classifier.calls = %d, want 1 (the opener must be classified)", classifier.calls)
	}
	if classifier.got != turns[0] {
		t.Fatalf("classifier was asked to classify %+v, want the window's real first turn %+v", classifier.got, turns[0])
	}
	codeThread, ok := store.threads[TopicType("code-topic")]
	if !ok {
		t.Fatalf("no thread created for %q: the classified opener was not resolved through the taxonomy", "code-topic")
	}
	if threadIDs[0] != codeThread {
		t.Fatalf("Route filed the opener under thread %q, want the code-topic thread %q", threadIDs[0], codeThread)
	}
	if _, filedUnderFallback := store.threads[TopicType("fallback")]; filedUnderFallback {
		t.Fatalf("a fallback thread was created for an opener with a real, classifiable topic - the pre-fix defect")
	}
}

// TestAutoThreaderOpenerWithNoSignalFallsBack is D5's second required
// proof: an opener the Classifier abstains on (label "") still resolves to
// the taxonomy's fallback - proving the fix does not remove the fallback
// path, only stops it from swallowing real content.
func TestAutoThreaderOpenerWithNoSignalFallsBack(t *testing.T) {
	seg := &fakeSegmenter{}
	classifier := &fakeClassifier{label: ""}
	store := newFakeThreadStore()
	at, err := NewAutoThreader(seg, classifier, store, testTaxonomy(),
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	turns := []Turn{{Speaker: "a", Text: "mmm"}}
	threadIDs, err := at.Route(context.Background(), turns)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(threadIDs) != 1 {
		t.Fatalf("Route returned %d thread ids, want 1", len(threadIDs))
	}
	fallbackThread, ok := store.threads[TopicType("fallback")]
	if !ok {
		t.Fatalf("no thread created for the fallback topic: a classifier abstention must still resolve there")
	}
	if threadIDs[0] != fallbackThread {
		t.Fatalf("Route filed the abstained opener under thread %q, want the fallback thread %q", threadIDs[0], fallbackThread)
	}
}

// TestNewDefaultAutoThreaderClassifiesOpenerOnce is D6's required
// counting-executor proof: through the REAL production wiring
// (NewDefaultAutoThreader, not fakeSegmenter/fakeClassifier doubles), a
// 1-turn Route window dispatches turn 0 through the executor exactly ONCE,
// and the label filed for the opener is the SAME label
// segmenterImpl.classifyAll used - proven here by taxonomy divergence, not
// by inspecting an internal field: the executor is loaded with two
// DIFFERENT labels ("code" for a would-be first dispatch, "docs" for a
// would-be second), each mapped by this test's own taxonomy to a distinct
// topic. If turn 0 were classified twice (the pre-D6 defect the confirming
// review's C2 finding describes), the second dispatch would consume
// "docs" and the opener would file under docs-topic - a DIFFERENT thread
// than the one classifyAll's own (discarded) label reasoned about,
// reproducing the exact divergence risk C2 flagged. With one shared,
// memoizing Classifier the second Classify call for the identical turn-0
// text is a cache hit: one request only, and the filed thread is
// code-topic - the label segmenterImpl.classifyAll actually dispatched for.
func TestNewDefaultAutoThreaderClassifiesOpenerOnce(t *testing.T) {
	exec := &fakeClassifyExecutor{labels: []string{"code", "docs"}}
	emb := &fakeEmbedder{vectors: [][]float32{{1, 0}}}
	store := newFakeThreadStore()
	taxonomy := NewTaxonomyConfig(
		map[string]TopicType{"code": TopicType("code-topic"), "docs": TopicType("docs-topic")},
		TopicType("fallback"),
	)
	at, err := NewDefaultAutoThreader(exec, emb, validCfg(), store, taxonomy,
		mustExemplarStore(t, storetest.NewMemStore()), &fakePublisher{}, newFixedClock())
	if err != nil {
		t.Fatalf("NewDefaultAutoThreader: %v", err)
	}
	turns := []Turn{{Speaker: "a", Text: "let's talk about code review"}}
	threadIDs, err := at.Route(context.Background(), turns)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(threadIDs) != 1 {
		t.Fatalf("Route returned %d thread ids, want 1", len(threadIDs))
	}
	if len(exec.requests) != 1 {
		t.Fatalf("classify executor received %d requests for turn 0's window, want exactly 1 - "+
			"the opener must be a memo-cache hit on the SAME Classifier classifyAll already dispatched "+
			"through, not a second independent dispatch (P1-E21-W5-S46-T5 D6)", len(exec.requests))
	}
	codeThread, ok := store.threads[TopicType("code-topic")]
	if !ok {
		t.Fatalf("no thread created for %q: the filed opener label was not the label classifyAll dispatched "+
			"(\"code\") - a second dispatch would have consumed \"docs\" instead and diverged", "code-topic")
	}
	if threadIDs[0] != codeThread {
		t.Fatalf("Route filed the opener under thread %q, want the code-topic thread %q", threadIDs[0], codeThread)
	}
	if _, filedUnderDocs := store.threads[TopicType("docs-topic")]; filedUnderDocs {
		t.Fatalf("a docs-topic thread was created: turn 0 was dispatched a second time and diverged from " +
			"classifyAll's own label - exactly the double-dispatch defect D6 fixes")
	}
}
