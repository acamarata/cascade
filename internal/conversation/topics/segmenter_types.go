// Purpose: the Segmenter interface, its Turn input type, the Boundary
//   result type, the Classifier interface, and (P1-E21-W5-S46-T5 D6,
//   2026-09-21) cheapLaneClassifier - Classifier's sole production
//   implementation - moved here from segmenter_core.go to keep that file
//   under the 300-line cap once NewSegmenterWith and the memo cache were
//   added; the interface and its one implementation living together is a
//   smaller exception to "contracts and data shapes, not behavior" than
//   splitting a single-implementation interface across two files.
// Inputs: none at the interface/type layer (contracts and data shapes);
//   cheapLaneClassifier's own inputs are a provider.ModelExecutor at
//   construction and a Turn per Classify call.
// Outputs: none at the interface/type layer; cheapLaneClassifier.Classify
//   returns a trimmed label or a typed error (segmenter_errors.go).
// Constraints: Turn moved here from eval.go (P1-E21-W5-S45-T1) under
//   P1-E21-W5-S45-T2's eval-harness-to-test-only move (R-14.283, 2026-09-20):
//   eval.go's other symbols (CorpusRecord, Corpus, LoadCorpus, Evaluate,
//   AssertFloors) have zero non-test callers and moved into
//   eval_harness_test.go, but Turn is this file's own Segment(ctx,
//   []Turn) signature and segmenter_core.go's production input type, so it
//   stays here rather than in a _test.go file. The corpus record format and
//   the segmenter's input turn remain the same shape by design, so a corpus
//   transcript can still be fed to Segment directly without a conversion
//   step. cheapLaneClassifier never imports internal/conductor and sets no
//   Policy/Sensitivity override, the same stance segmenter_core.go's own
//   header documents for buildClassifyRequest.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Turn is one speaker turn handed to Segment, and the same shape a corpus
// transcript record uses (see eval_harness_test.go's CorpusRecord) so a
// corpus transcript can be fed to Segment directly without a conversion
// step.
type Turn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// Segmenter finds topic boundaries across an ordered window of turns: the
// four-component engine (cheap-lane classify, embedding-distance boundary
// detection, a hysteresis filter, and a bounded topic stack) that
// segmenterImpl (segmenter_core.go) implements per 05-PEWS-PLAN-W4-W6.md
// §Epic U S-45.T2. It is declared as an interface, rather than exposing
// *segmenterImpl directly, so S-45.T3's AutoThreader can depend on this
// narrow contract and substitute a test double without importing this
// package's construction machinery (the same shape composer_types.go's
// SummarizerGetter and this package's own eval.go already use in this
// tree).
type Segmenter interface {
	// Segment scans turns in order and returns the boundaries the engine
	// commits: every element of turns before the first Boundary's
	// TurnIndex (and, between any two boundaries, every turn from one
	// Boundary's TurnIndex up to the next) belongs to one topic segment.
	// A nil or empty turns is not an error: Segment returns (nil, nil).
	// A canceled ctx returns KindCanceled and an expired one
	// KindTimeout, rather than a partial result. A classify or embed
	// dependency failure propagates as a typed error (see
	// segmenter_core.go's errSegmenterDependency), and an embedding
	// batch that violates the Embedder contract (wrong count, wrong
	// model, wrong width, or a zero-norm vector) is refused with
	// KindInvalidInput; Segment never swallows either into a degraded
	// result or a fabricated distance.
	//
	// Segment is a pure function of the window it is handed: the same
	// turns, doubles, and config always yield the same boundaries, and
	// nothing is carried between calls.
	Segment(ctx context.Context, turns []Turn) ([]Boundary, error)
}

// Boundary is one topic boundary Segment commits: turns[TurnIndex] is the
// first turn of a new topic segment, classified under Label by the
// cheap-lane classifier. Boundary's two fields deliberately mirror the
// corpus-accuracy harness's CorpusRecord shape (Boundaries []int,
// TopicLabels map[int]string; see eval_harness_test.go), so a caller
// scoring predictions against the corpus (see segmenter_corpus_test.go)
// builds an EvalPredictions directly from a []Boundary without an
// intermediate translation type.
type Boundary struct {
	// TurnIndex is the 0-based index of the FIRST TURN OF THE NEW
	// SEGMENT: the turn whose distance from its predecessor cleared the
	// threshold, never the later turn at which a confirmation window
	// finished being observed. TurnIndex is always >= 1 - the first turn
	// (index 0) starts the first segment implicitly and is never itself
	// reported as a boundary.
	//
	// PROVISIONAL TAIL BOUNDARIES. When a boundary is committed within
	// HysteresisConfig.Window-1 turns of the end of the window, its
	// confirmation window extends past the last turn and cannot be
	// observed; it is committed provisionally on the evidence available
	// (hysteresis.go's confirm states why withholding it would be
	// worse). A caller that re-segments a longer window may therefore
	// see such a boundary disappear, which is the only case where
	// extending the input changes an earlier boundary.
	TurnIndex int
	// Label is the cheap-lane classifier's topic label for the segment
	// that starts at TurnIndex, exactly as the classifier returned it
	// (trimmed of surrounding whitespace, never re-mapped or
	// canonicalized here - taxonomy resolution to a canonical TopicType
	// is S-45.T3's TaxonomyConfig, out of this ticket's scope).
	Label string
}

// Classifier is the cheap-lane topic-classify seam segmenterImpl.classifyAll
// (segmenter_core.go) dispatches through on every turn. Extracted to its
// own interface (P1-E21-W5-S46-T5 D5) so AutoThreader (auto_thread.go) can
// classify a turn - specifically, a routed window's implicit opening
// segment, which the Segmenter never labels (this file's Boundary doc
// above) - with the IDENTICAL dispatch segmenter_core.go uses for every
// other turn, rather than a second implementation that could drift from
// this one. cheapLaneClassifier (below) is the only production
// implementation; NewClassifier builds one over a provider.ModelExecutor.
type Classifier interface {
	// Classify returns turn's trimmed cheap-lane topic label. An empty
	// response (a provider that answered with only whitespace) is a
	// KindIntegrity failure, not a valid empty topic; a dispatch failure
	// propagates through errSegmenterDependency.
	Classify(ctx context.Context, turn Turn) (string, error)
}

// classifierMemoCap bounds cheapLaneClassifier's per-instance memo cache
// (P1-E21-W5-S46-T5 D6). 64 is small enough that the cache never becomes a
// meaningful memory cost (each entry is at most a turn's text plus a short
// label string) and large enough to cover the actual reuse pattern this
// cache exists for: a single Route window's turn 0 is classified once by
// segmenterImpl.classifyAll and, within the SAME window, once more by
// AutoThreader.classifyOpener (auto_thread.go) - two lookups, not sixty-four
// - so any cap past "a handful of windows deep" is already generous. Oldest
// entry evicted first (FIFO via the order slice below): this is a dispatch
// dedup cache, not an LRU meant to model access recency, so the simpler
// policy is preferable.
const classifierMemoCap = 64

// cheapLaneClassifier is Classifier's sole production implementation:
// one classify dispatch per turn over a provider.ModelExecutor - task_class
// "classify", the §5.16 row's advisory Requirements, and no
// Policy/Sensitivity override (see this file's header comment) - exactly
// segmenterImpl's former classify/buildClassifyRequest pair, moved here
// unchanged. MEMOIZED BY TURN TEXT (P1-E21-W5-S46-T5 D6): a successful
// classification is cached under turn.Text, bounded to classifierMemoCap
// entries with oldest-first eviction, guarded by mu so concurrent Classify
// calls on one shared instance (segmenterImpl.classifyAll and
// AutoThreader.classifyOpener both hold the SAME *cheapLaneClassifier once
// NewDefaultAutoThreader wires it - auto_thread.go) never race the map. A
// dispatch failure or an empty-label refusal is never cached, so a
// transient provider error does not poison future calls for that text.
type cheapLaneClassifier struct {
	executor provider.ModelExecutor

	mu    sync.Mutex
	cache map[string]string
	order []string // insertion order, oldest first, for FIFO eviction
}

// NewClassifier wraps executor as a Classifier with its own bounded memo
// cache. A composition root that wants segmenterImpl and AutoThreader to
// share one cache (and therefore dispatch a repeated turn's text only
// once) must call this ONCE and hand the same value to both
// NewSegmenterWith and NewAutoThreader - see NewSegmenterWith's own doc.
func NewClassifier(executor provider.ModelExecutor) Classifier {
	return &cheapLaneClassifier{executor: executor}
}

var _ Classifier = (*cheapLaneClassifier)(nil)

// Classify implements Classifier. A cache hit on turn.Text returns the
// memoized label with no dispatch at all.
func (c *cheapLaneClassifier) Classify(ctx context.Context, turn Turn) (string, error) {
	if label, ok := c.memoGet(turn.Text); ok {
		return label, nil
	}
	req, err := c.buildClassifyRequest(turn)
	if err != nil {
		return "", err
	}
	resp, err := c.executor.Execute(ctx, req)
	if err != nil {
		return "", errSegmenterDependency(err, "topics: segment: classify dispatch failed")
	}
	label := strings.TrimSpace(resp.Output)
	if label == "" {
		return "", errSegmenterEmptyLabel()
	}
	c.memoPut(turn.Text, label)
	return label, nil
}

// memoGet returns the cached label for text, if any, under mu.
func (c *cheapLaneClassifier) memoGet(text string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	label, ok := c.cache[text]
	return label, ok
}

// memoPut records text->label under mu, evicting the oldest entry first
// when the cache is already at classifierMemoCap. A text already present
// (a race that lost memoGet's check) is left as-is rather than reinserted,
// so order never grows a duplicate entry for one key.
func (c *cheapLaneClassifier) memoPut(text, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.cache[text]; exists {
		return
	}
	if c.cache == nil {
		c.cache = make(map[string]string, classifierMemoCap)
	}
	if len(c.order) >= classifierMemoCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, oldest)
	}
	c.cache[text] = label
	c.order = append(c.order, text)
}

// buildClassifyRequest builds the ModelRequest one Classify call dispatches.
func (c *cheapLaneClassifier) buildClassifyRequest(turn Turn) (provider.ModelRequest, error) {
	id, err := cascade.NewID()
	if err != nil {
		return provider.ModelRequest{}, cascade.Wrap(cascade.KindInternal, err, "topics: segment: minting task id")
	}
	return provider.ModelRequest{
		TaskID:    "topics-segmenter-" + string(id),
		TaskClass: classifyTaskClass,
		Inputs:    []provider.ChatMessage{{Role: "user", Content: fmt.Sprintf(classifyPrompt, turn.Text)}},
		Requirements: provider.Requirements{
			Reasoning: classifyReasoning,
			Context:   classifyContextTokens,
		},
	}, nil
}
