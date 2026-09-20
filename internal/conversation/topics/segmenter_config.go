// Purpose: HysteresisConfig (the segmenter constructor's third parameter,
//   per this ticket's own HOW section: "a HysteresisConfig (threshold,
//   window N)"), its validation, and the classify-dispatch constants
//   segmenter_core.go's classify() builds a provider.ModelRequest from.
// Inputs: none at this layer - config values arrive from the caller at
//   construction.
// Outputs: none.
// Constraints: classifyTaskClass is a local string literal, never
//   internal/conductor.TaskClassClassify - production code in this package
//   never imports internal/conductor, the same stance
//   internal/context/summarizer_dispatch.go already takes for its own
//   task-class literal (see that file's header comment). The literal
//   cannot silently drift from the §5.16 taxonomy: segmenter_core_test.go
//   asserts classifyTaskClass == string(conductor.TaskClassClassify) from
//   a _test.go file, where the import is safe.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

import (
	"math"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HysteresisConfig configures the hysteresis filter segmenter_core.go
// builds internally: Threshold is the normalized-cosine-distance floor a
// turn-to-turn embedding jump must clear to count as a candidate boundary,
// and Window is N, the length of the confirmation window (the candidate
// transition plus the N-1 transitions after it) over which the new topic
// must hold before the Boundary commits.
type HysteresisConfig struct {
	// Threshold is compared against 1-cosineSimilarity(prev, cur) for
	// each consecutive turn pair; a value at or above Threshold is a
	// candidate distance spike. Must be > 0 and a real (non-NaN, non-Inf)
	// number. Zero is refused, not treated as "very sensitive": every
	// pair of non-identical vectors has a distance above 0, so a
	// Threshold of 0 makes EVERY transition a spike and turns the
	// distance signal off - the caller who wrote 0 meant a sensitive
	// threshold, not no threshold, and gets told so instead of silently
	// getting a segmenter that splits on noise. Validate enforces no
	// upper bound: cosine distance never exceeds 2, so a deliberately
	// unreachable threshold (segmentation effectively disabled) is a
	// choice, not a config error.
	Threshold float64
	// Window is N: the confirmation window length in transitions.
	// Window=1 commits on the candidate spike alone (hysteresis
	// disabled) - a valid, if aggressive, configuration, not an error.
	// Window=2 additionally requires the next transition not to spike,
	// and so on. Must be >= 1.
	Window int
}

// Validate reports a *cascade.Error with KindInvalidInput when c cannot be
// used to build a hysteresis filter: a Window below 1 (there is no such
// thing as a zero-length confirmation window), or a Threshold that is
// zero, negative, NaN, or infinite (none of which is ever a meaningful
// cosine-distance floor - see the field comments for why 0 is refused
// rather than accepted as "maximally sensitive").
func (c HysteresisConfig) Validate() error {
	if c.Window < 1 {
		return cascade.Newf(cascade.KindInvalidInput,
			"topics: HysteresisConfig.Window must be >= 1, got %d", c.Window)
	}
	if c.Threshold <= 0 || math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) {
		return cascade.Newf(cascade.KindInvalidInput,
			"topics: HysteresisConfig.Threshold must be a finite number > 0, got %v", c.Threshold)
	}
	return nil
}

// defaultTopicStackDepth is the bounded topic stack's max depth
// segmenter_core.go's NewSegmenter uses when constructing its internal
// stack. Not a NewSegmenter parameter (the ticket's constructor signature
// is executor, embedder, HysteresisConfig - exactly three parameters, no
// more): the stack's depth is an internal tuning constant of the engine,
// not a caller-facing knob, matching how the classify-dispatch constants
// below are internal to classify() rather than constructor parameters.
const defaultTopicStackDepth = 8

// The §5.16 taxonomy row for the classify task class (internal/conductor/
// task_classes.go): Reasoning "low", CtxK 8 (=> 8000 tokens),
// LaneAffinity "cheapest/free". Requirements values are advisory - the
// router does not exclude a lane on Requirements, only on
// RequiredCapabilities - so setting them never excludes a lane; they exist
// for fidelity to the taxonomy row, the same reasoning
// summarizer_dispatch.go's identical comment gives for its own constants.
const (
	classifyTaskClass     = "classify"
	classifyReasoning     = "low"
	classifyContextTokens = 8000
)

// classifyPrompt is the single prompt template classify() renders: asks
// for one short topic label and nothing else, matching the §5.16 row's
// Structured=true expectation (a constrained, single-token-ish answer)
// without this ticket inventing a JSON-schema request field no provider
// driver in this tree implements yet.
const classifyPrompt = "Classify the dominant topic of the following conversation turn. " +
	"Respond with a single short topic label only, no punctuation, no explanation.\n\nTURN:\n%s"
