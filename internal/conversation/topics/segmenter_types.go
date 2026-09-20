// Purpose: the Segmenter interface, its Turn input type, and the Boundary
//   result type that the rest of this ticket's files (segmenter_config.go,
//   segmenter_core.go, hysteresis.go, topic_stack.go) implement and
//   consume. Kept in its own file, separate from the constructor and the
//   Segment logic, per files_scope.
// Inputs: none at this layer - these are contracts and data shapes, not
//   behavior.
// Outputs: none.
// Constraints: Turn moved here from eval.go (P1-E21-W5-S45-T1) under
//   P1-E21-W5-S45-T2's eval-harness-to-test-only move (R-14.283, 2026-09-20):
//   eval.go's other symbols (CorpusRecord, Corpus, LoadCorpus, Evaluate,
//   AssertFloors) have zero non-test callers and moved into
//   eval_harness_test.go, but Turn is this file's own Segment(ctx,
//   []Turn) signature and segmenter_core.go's production input type, so it
//   stays here rather than in a _test.go file. The corpus record format and
//   the segmenter's input turn remain the same shape by design, so a corpus
//   transcript can still be fed to Segment directly without a conversion
//   step.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

import "context"

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
