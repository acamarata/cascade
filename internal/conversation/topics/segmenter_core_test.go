// Package topics (segmenter_core_test.go): Purpose: fakeClassifyExecutor
// and fakeEmbedder (real, in-memory provider.ModelExecutor/provider.
// Embedder test doubles, Art.1 - _test.go only, never a shipped mock), the
// shared turn/config helpers and the assertCause identity check every test
// file in this package uses, NewSegmenter's construction-time validation,
// and Segment's non-hysteresis behavior: stable topic, stack bounds, empty
// turns, nil ctx, cancellation, deadline mapping, and platform parity. The
// dependency/contract refusals live in segmenter_errors_test.go, the
// hysteresis index scenarios in hysteresis_test.go, and the corpus-scoring
// harness in segmenter_harness_test.go. Deterministic, no network, no
// sleeps.
package topics

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeClassifyExecutor returns labels[i] for the i-th Execute call, in
// call order (Segment always classifies turns strictly in sequence, so
// indexing by call order is equivalent to indexing by turn and needs no
// content parsing). cancelAfter, when set, cancels ctx immediately after
// producing the response for the call whose 1-based number equals it -
// used to prove Segment's per-turn ctx.Err() check is not dead code.
type fakeClassifyExecutor struct {
	mu          sync.Mutex
	labels      []string
	err         error
	requests    []provider.ModelRequest
	cancelAfter int
	cancel      context.CancelFunc
}

func (f *fakeClassifyExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	call := len(f.requests)
	f.mu.Unlock()
	if f.err != nil {
		return provider.ModelResponse{}, f.err
	}
	if call > len(f.labels) {
		return provider.ModelResponse{}, cascade.New(cascade.KindInternal, "fakeClassifyExecutor: no label configured")
	}
	out := provider.ModelResponse{Output: f.labels[call-1]}
	if f.cancelAfter == call && f.cancel != nil {
		f.cancel()
	}
	return out, nil
}

// fakeEmbedder returns exactly len(vectors) outputs regardless of the
// input batch size, so a test can configure a short batch, a wrong-width
// vector, or (via outModel) outputs tagged with a different embedding space
// and exercise embedAll's real ValidBatch refusal rather than a fake one.
type fakeEmbedder struct {
	vectors  [][]float32
	err      error
	outModel *provider.EmbedModel
}

var testEmbedModel = provider.EmbedModel{ID: "topics-test-embed-v1", Dimensions: 2}

func (f *fakeEmbedder) Model() provider.EmbedModel { return testEmbedModel }

func (f *fakeEmbedder) Embed(_ context.Context, _ []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	tag := testEmbedModel
	if f.outModel != nil {
		tag = *f.outModel
	}
	out := make([]provider.EmbedOutput, len(f.vectors))
	for i, v := range f.vectors {
		out[i] = provider.EmbedOutput{Vector: v, Model: tag}
	}
	return out, nil
}

// turnsN returns n turns with DISTINCT text (P1-E21-W5-S46-T5 D6, scope
// deviation - see the ticket's narrow-fix report): before D6, every turn
// here shared the literal text "turn text", which was harmless because
// nothing cached on turn content. D6 gives cheapLaneClassifier
// (segmenter_types.go) a bounded per-instance memo cache keyed by
// turn.Text, so identical text across turns in ONE window would collapse
// classifyAll's "one classify call per turn" contract (segmenter_core.go)
// into a single dispatch reused for every turn - not the cross-call reuse
// D6 targets (segmenterImpl.classifyAll's own pass classifying turn 0,
// followed by AutoThreader.classifyOpener reclassifying the SAME turn 0
// value again). Distinct text per turn removes the accidental collision
// while every existing assertion in this package is indifferent to the
// literal text (only turn COUNT, POSITION, and the fake doubles' labels/
// vectors are asserted anywhere in this package).
func turnsN(n int) []Turn {
	turns := make([]Turn, n)
	for i := range turns {
		turns[i] = Turn{Speaker: "a", Text: "turn text " + strconv.Itoa(i)}
	}
	return turns
}

func validCfg() HysteresisConfig { return HysteresisConfig{Threshold: 0.5, Window: 2} }

// assertCause walks got's error chain looking for want by identity. Kind
// alone is not enough: every error this package returns carries a Kind, and
// (*cascade.Error).Is matches on Kind, so a Kind-only assertion cannot tell
// a propagated dependency failure from a locally-invented error of the same
// Kind - which is exactly what an error-propagation test has to prove.
func assertCause(t *testing.T, got, want error) {
	t.Helper()
	for err := got; err != nil; err = errors.Unwrap(err) {
		if err == want {
			return
		}
	}
	t.Fatalf("error %v does not carry the injected cause %v anywhere in its chain", got, want)
}

func TestSegmenterClassifyTaskClassMatchesTaxonomy(t *testing.T) {
	if classifyTaskClass != string(conductor.TaskClassClassify) {
		t.Fatalf("classifyTaskClass = %q, want conductor.TaskClassClassify = %q",
			classifyTaskClass, conductor.TaskClassClassify)
	}
}

func TestSegmenter_NewRejectsInvalidDependencies(t *testing.T) {
	valid := &fakeEmbedder{}
	cases := []struct {
		name     string
		exec     provider.ModelExecutor
		embedder provider.Embedder
		cfg      HysteresisConfig
	}{
		{"nil executor", nil, valid, validCfg()},
		{"nil embedder", &fakeClassifyExecutor{}, nil, validCfg()},
		{"zero window", &fakeClassifyExecutor{}, valid, HysteresisConfig{Threshold: 0.5, Window: 0}},
		{"zero threshold", &fakeClassifyExecutor{}, valid, HysteresisConfig{Threshold: 0, Window: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := newTestSegmenter(c.exec, c.embedder, c.cfg); !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("newTestSegmenter(%s) error = %v, want KindInvalidInput", c.name, err)
			}
		})
	}
}

func TestSegmenter_NewAcceptsValidDependencies(t *testing.T) {
	s, err := newTestSegmenter(&fakeClassifyExecutor{}, &fakeEmbedder{}, validCfg())
	if err != nil || s == nil {
		t.Fatalf("NewSegmenter valid deps: got (%v, %v), want a usable Segmenter", s, err)
	}
}

func TestSegmenter_NilContextAndEmptyTurns(t *testing.T) {
	s, _ := newTestSegmenter(&fakeClassifyExecutor{}, &fakeEmbedder{}, validCfg())
	if _, err := s.Segment(nil, turnsN(1)); !cascade.HasKind(err, cascade.KindInvalidInput) { //nolint:staticcheck
		t.Fatalf("nil ctx: error = %v, want KindInvalidInput", err)
	}
	for name, turns := range map[string][]Turn{"nil": nil, "empty": {}} {
		bounds, err := s.Segment(context.Background(), turns)
		if err != nil || bounds != nil {
			t.Fatalf("%s turns: got (%v, %v), want (nil, nil)", name, bounds, err)
		}
	}
}

func TestSegmenter_NoBoundaryWhenTopicStable(t *testing.T) {
	exec := &fakeClassifyExecutor{labels: []string{"A", "A", "A", "A"}}
	emb := &fakeEmbedder{vectors: [][]float32{{1, 0}, {1, 0}, {1, 0}, {1, 0}}}
	s, _ := newTestSegmenter(exec, emb, validCfg())
	bounds, err := s.Segment(context.Background(), turnsN(4))
	if err != nil || len(bounds) != 0 {
		t.Fatalf("stable topic: got (%v, %v), want (nil boundaries, nil error)", bounds, err)
	}
}

// TestSegmenter_StackBoundsAcrossManyCommits drives more distinct topics
// through the stack than its max depth, at Window=1 so every transition
// commits, and asserts the engine keeps producing boundaries after the
// stack has started evicting - a stack that grew unbounded and one that
// dropped the current topic on eviction would both be invisible without
// running past the depth.
func TestSegmenter_StackBoundsAcrossManyCommits(t *testing.T) {
	n := defaultTopicStackDepth + 5
	labels := make([]string, n)
	vectors := make([][]float32, n)
	for i := range labels {
		labels[i] = strings.Repeat("x", i+1) // a distinct label every turn
		if i%2 == 0 {
			vectors[i] = []float32{1, 0}
		} else {
			vectors[i] = []float32{0, 1}
		}
	}
	exec := &fakeClassifyExecutor{labels: labels}
	emb := &fakeEmbedder{vectors: vectors}
	s, _ := newTestSegmenter(exec, emb, HysteresisConfig{Threshold: 0.5, Window: 1})
	bounds, err := s.Segment(context.Background(), turnsN(n))
	if err != nil {
		t.Fatalf("many distinct topics past stack depth: unexpected error %v", err)
	}
	if len(bounds) != n-1 {
		t.Fatalf("many distinct topics: got %d boundaries, want %d (every transition after the first commits)",
			len(bounds), n-1)
	}
}

func TestSegmenter_ContextCanceledMidLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := &fakeClassifyExecutor{labels: []string{"A", "A"}, cancelAfter: 1, cancel: cancel}
	emb := &fakeEmbedder{vectors: [][]float32{{1, 0}, {1, 0}}}
	s, _ := newTestSegmenter(exec, emb, validCfg())
	_, err := s.Segment(ctx, turnsN(2))
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("mid-loop cancellation: got %v, want KindCanceled", err)
	}
	assertCause(t, err, context.Canceled)
	exec.mu.Lock()
	calls := len(exec.requests)
	exec.mu.Unlock()
	if calls != 1 {
		t.Fatalf("mid-loop cancellation: classify was called %d times, want exactly 1 (the check must run "+
			"BEFORE dispatching the next turn)", calls)
	}
}

// TestSegmenter_DeadlineExceededMapsToTimeout keeps the two context failures
// distinguishable: a caller retries a timeout with a longer budget and
// never retries a cancellation, so collapsing both into KindCanceled (or
// both into KindTimeout) would be a wrong answer in one of the two cases.
func TestSegmenter_DeadlineExceededMapsToTimeout(t *testing.T) {
	// A zero timeout is already expired when WithTimeout returns, so the
	// deadline fires without a sleep and without a clock (Art.7.3).
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	exec := &fakeClassifyExecutor{labels: []string{"A"}}
	s, _ := newTestSegmenter(exec, &fakeEmbedder{vectors: [][]float32{{1, 0}}}, validCfg())
	_, err := s.Segment(ctx, turnsN(1))
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("expired deadline: got %v, want KindTimeout", err)
	}
	assertCause(t, err, context.DeadlineExceeded)
	exec.mu.Lock()
	calls := len(exec.requests)
	exec.mu.Unlock()
	if calls != 0 {
		t.Fatalf("expired deadline: classify was called %d times, want 0", calls)
	}
}

// TestSegmenterPlatformParity is Art.5's explicit per-platform assertion:
// this package is pure Go with no CGO, no OS-specific file paths, and no
// platform-conditional branch anywhere in its production files, so the CI
// matrix (macOS, Linux, Windows) running this SAME named test to a PASS on
// all three is the "explicit CI result" Art.5 requires, rather than an
// unstated assumption that cross-platform Go needs no such check. The
// assertion itself is real and platform-independent: two turns with a
// label change and a distance spike must commit exactly one boundary at
// index 1, on every OS/ARCH this ticket ships to.
func TestSegmenterPlatformParity(t *testing.T) {
	t.Logf("topics segmenter platform parity: GOOS=%s GOARCH=%s", runtime.GOOS, runtime.GOARCH)
	exec := &fakeClassifyExecutor{labels: []string{"a", "b"}}
	emb := &fakeEmbedder{vectors: [][]float32{{1, 0}, {0, 1}}}
	s, err := newTestSegmenter(exec, emb, HysteresisConfig{Threshold: 0.1, Window: 1})
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	bounds, err := s.Segment(context.Background(), turnsN(2))
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	want := Boundary{TurnIndex: 1, Label: "b"}
	if len(bounds) != 1 || bounds[0] != want {
		t.Fatalf("platform parity on %s/%s: got %v, want exactly [%v]", runtime.GOOS, runtime.GOARCH, bounds, want)
	}
}
