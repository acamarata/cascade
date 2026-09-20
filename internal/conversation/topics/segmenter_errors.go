// Purpose: every typed error the segmenter returns, in one place: the
//   construction refusals, Segment's caller-misuse refusals, the
//   embed-contract refusal, the context-error mapping, and the
//   dependency-failure wrapper. Split out of segmenter_core.go under the
//   300-line file cap (Art.10.3), following the same
//   summarizer_errors.go / summarizer_dispatch.go split
//   internal/context already uses for the same reason.
// Inputs: the values each message names (a model identity, a batch, a
//   context error).
// Outputs: *cascade.Error values carrying the frozen Kinds pkg/cascade
//   declares.
// Constraints: no I/O, no clock; messages name what was observed so a
//   failing provider is identifiable from the message alone, and never
//   include turn text (a turn can carry user content).
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// errSegmenterNilExecutor reports that NewSegmenter was given a nil
// provider.ModelExecutor.
func errSegmenterNilExecutor() error {
	return cascade.New(cascade.KindInvalidInput, "topics: NewSegmenter: ModelExecutor must not be nil")
}

// errSegmenterNilEmbedder reports that NewSegmenter was given a nil
// provider.Embedder.
func errSegmenterNilEmbedder() error {
	return cascade.New(cascade.KindInvalidInput, "topics: NewSegmenter: Embedder must not be nil")
}

// errSegmenterNilContext reports a nil ctx passed to Segment.
func errSegmenterNilContext() error {
	return cascade.New(cascade.KindInvalidInput, "topics: Segment: ctx must not be nil")
}

// errSegmenterEmptyLabel reports a classify response with no usable label.
func errSegmenterEmptyLabel() error {
	return cascade.New(cascade.KindIntegrity, "topics: segment: classifier returned an empty label")
}

// errSegmenterContext maps a context error to the Kind the Segmenter
// interface promises: KindTimeout for an expired deadline, KindCanceled
// for a canceled context. The two are not interchangeable to a caller -
// a timeout is retryable with a longer budget, a cancellation is not - and
// (*cascade.Error).Is compares Kind only, so collapsing both into
// KindCanceled would make them indistinguishable downstream.
func errSegmenterContext(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return cascade.Wrap(cascade.KindTimeout, err, "topics: segment: context deadline exceeded")
	}
	return cascade.Wrap(cascade.KindCanceled, err, "topics: segment: canceled")
}

// errSegmenterEmbedBatchInvalid reports an embedding batch that fails
// EmbedModel.ValidBatch, naming what was received so the failing
// implementation is identifiable from the message alone.
func errSegmenterEmbedBatchInvalid(
	model provider.EmbedModel, inputs []provider.EmbedInput, outputs []provider.EmbedOutput,
) error {
	widths := make([]int, len(outputs))
	sameModel := true
	for i, out := range outputs {
		widths[i] = len(out.Vector)
		if !out.Model.Equal(model) {
			sameModel = false
		}
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"topics: segment: embedder violated its own batch contract: %d inputs, %d outputs, "+
			"vector widths %v, all outputs tagged model %s/%d: %t",
		len(inputs), len(outputs), widths, model.ID, model.Dimensions, sameModel)
}

// errSegmenterDependency wraps a dependency failure, preserving its Kind
// when it already carries one - the same errSummarizerDependency precedent
// internal/context/summarizer_errors.go uses for the identical reason:
// (*cascade.Error).Is compares Kind only, so preserving the real Kind is
// what lets a caller distinguish, say, a timeout from a quota exhaustion.
func errSegmenterDependency(err error, msg string) error {
	if k, ok := cascade.KindOf(err); ok {
		return cascade.Wrap(k, err, msg)
	}
	return cascade.Wrap(cascade.KindInternal, err, msg)
}
