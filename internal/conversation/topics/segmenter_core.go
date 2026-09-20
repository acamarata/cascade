// Purpose: segmenterImpl, NewSegmenter, and the Segment pipeline that
//   drives the four-component engine: batch-embeds every turn and derives
//   the per-transition distance-spike map (component 2), classifies each
//   turn on the cheap lane (component 1), then walks the transitions
//   asking the hysteresis filter (component 3) which candidates commit,
//   keeping the current topic on the bounded stack (component 4).
// Inputs: a provider.ModelExecutor (the sole model.execute door, K/S-22.T1
//   - injected as the interface, never internal/conductor.Executor
//   directly, the same seam internal/context.Summarizer already uses), a
//   provider.Embedder (F/S-10.T5), and a HysteresisConfig, all required at
//   construction; a []Turn per Segment call.
// Outputs: []Boundary, or a typed error for caller misuse, a dependency
//   failure, or an embedding batch that violates the Embedder contract.
// Constraints: production code in this file NEVER imports
//   internal/conductor (see segmenter_config.go's header comment).
//   SENSITIVITY IS INHERITED, NOT DECLARED HERE: buildClassifyRequest sets
//   no Policy and leaves Sensitivity at its zero value
//   (provider.SensitivityRestricted by declaration, R-21.264), the
//   fail-closed default 06-FORGE-SPEC.md §5.16 requires when a request's
//   own sensitivity cannot be resolved locally. The conductor's own
//   privacy filter applies the calling thread's actual tier from ctx
//   regardless of what this request carries; this file's job is only to
//   never loosen that by setting an explicit permissive field - the exact
//   stance internal/context/summarizer_dispatch.go's identical comment
//   documents for the same reason.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// segmenterImpl is the concrete Segmenter. Build one with NewSegmenter; the
// zero value is not usable (a nil executor or embedder can never dispatch).
type segmenterImpl struct {
	executor   provider.ModelExecutor
	embedder   provider.Embedder
	hysteresis HysteresisConfig
}

var _ Segmenter = (*segmenterImpl)(nil)

// NewSegmenter validates its three required dependencies and returns a
// ready Segmenter. executor and embedder must be non-nil, and cfg must
// pass HysteresisConfig.Validate - a partially-configured segmenter (no
// dispatch door, no vector source, or a threshold/window that could never
// decide anything) is refused at construction rather than failing
// confusingly on the first Segment call.
func NewSegmenter(executor provider.ModelExecutor, embedder provider.Embedder, cfg HysteresisConfig) (Segmenter, error) {
	if executor == nil {
		return nil, errSegmenterNilExecutor()
	}
	if embedder == nil {
		return nil, errSegmenterNilEmbedder()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &segmenterImpl{executor: executor, embedder: embedder, hysteresis: cfg}, nil
}

// Segment implements the Segmenter interface (segmenter_types.go). It runs
// in four stages, in this order: embed the whole window in one batch,
// reduce the vectors to a per-transition spike map, classify every turn,
// then decide which candidate transitions commit. Embedding first means a
// batch that violates the Embedder contract is refused before a single
// classify call is dispatched, so a broken embedding provider costs no
// model spend.
func (s *segmenterImpl) Segment(ctx context.Context, turns []Turn) ([]Boundary, error) {
	if ctx == nil {
		return nil, errSegmenterNilContext()
	}
	if len(turns) == 0 {
		return nil, nil
	}
	vectors, err := s.embedAll(ctx, turns)
	if err != nil {
		return nil, err
	}
	spikes, err := s.spikeMap(vectors)
	if err != nil {
		return nil, err
	}
	filter, err := newHysteresisFilter(s.hysteresis)
	if err != nil {
		return nil, err
	}
	labels, err := s.classifyAll(ctx, turns)
	if err != nil {
		return nil, err
	}
	return commitBoundaries(labels, spikes, filter), nil
}

// embedAll batch-embeds every turn's text in one Embed call and returns
// the resulting vectors, positionally corresponding to turns. The response
// is checked against the Embedder contract's structural half with
// EmbedModel.ValidBatch - one output per input, every output carrying this
// Embedder's own Model, every vector exactly Model().Dimensions wide - the
// same refusal internal/retrieval/embed's pipeline.embed makes for the same
// reason: an implementation that silently switched models or truncated a
// vector produces plausible cosine distances, so the mismatch is
// undetectable after the fact and must be caught at the boundary.
func (s *segmenterImpl) embedAll(ctx context.Context, turns []Turn) ([][]float32, error) {
	inputs := make([]provider.EmbedInput, len(turns))
	for i, t := range turns {
		inputs[i] = provider.EmbedInput{Text: t.Text}
	}
	outputs, err := s.embedder.Embed(ctx, inputs)
	if err != nil {
		return nil, errSegmenterDependency(err, "topics: segment: embed dispatch failed")
	}
	model := s.embedder.Model()
	if !model.ValidBatch(inputs, outputs) {
		return nil, errSegmenterEmbedBatchInvalid(model, inputs, outputs)
	}
	vectors := make([][]float32, len(outputs))
	for i, out := range outputs {
		vectors[i] = out.Vector
	}
	return vectors, nil
}

// spikeMap reduces the window's vectors to one boolean per transition:
// out[i] reports whether the normalized cosine distance between turns i-1
// and i cleared HysteresisConfig.Threshold. out[0] is always false - turn 0
// has no predecessor to differ from - which keeps the index of a spike the
// index of the turn that would START the new segment, the index Boundary
// reports.
func (s *segmenterImpl) spikeMap(vectors [][]float32) ([]bool, error) {
	out := make([]bool, len(vectors))
	for i := 1; i < len(vectors); i++ {
		d, err := cosineDistance(vectors[i-1], vectors[i])
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, err,
				"topics: segment: comparing turns %d and %d", i-1, i)
		}
		out[i] = d >= s.hysteresis.Threshold
	}
	return out, nil
}

// classifyAll dispatches one cheap-lane classify call per turn, in order,
// and returns the labels positionally. ctx is checked before each dispatch so
// a canceled or expired context stops the scan instead of spending the
// remaining calls.
func (s *segmenterImpl) classifyAll(ctx context.Context, turns []Turn) ([]string, error) {
	labels := make([]string, len(turns))
	for i, turn := range turns {
		if cErr := ctx.Err(); cErr != nil {
			return nil, errSegmenterContext(cErr)
		}
		label, err := s.classify(ctx, turn)
		if err != nil {
			return nil, err
		}
		labels[i] = label
	}
	return labels, nil
}

// commitBoundaries walks the transitions and returns the boundaries the
// engine commits. A transition is a CANDIDATE when it spikes and the
// classifier's label there differs from the last COMMITTED topic (the
// stack's top, component 4) rather than from the immediately preceding
// turn's raw label, so a single mislabeled turn inside a topic cannot open
// a new segment on its own. The hysteresis filter then confirms or rejects
// the candidate from what follows it; a committed candidate is reported at
// its own index - the first turn of the new segment - and pushed onto the
// stack.
func commitBoundaries(labels []string, spikes []bool, filter *hysteresisFilter) []Boundary {
	stack := newTopicStack(defaultTopicStackDepth)
	stack.Push(topicID(labels[0]))
	var boundaries []Boundary
	for i := 1; i < len(labels); i++ {
		if !spikes[i] {
			continue
		}
		if current, _ := stack.Peek(); current == topicID(labels[i]) {
			continue
		}
		if !filter.confirm(spikes, i) {
			continue
		}
		boundaries = append(boundaries, Boundary{TurnIndex: i, Label: labels[i]})
		stack.Push(topicID(labels[i]))
	}
	return boundaries
}

// classify dispatches one cheap-lane classify call for turn and returns
// its trimmed label. An empty label (a provider that answered with only
// whitespace) is a KindIntegrity failure, not a valid empty topic.
func (s *segmenterImpl) classify(ctx context.Context, turn Turn) (string, error) {
	req, err := s.buildClassifyRequest(turn)
	if err != nil {
		return "", err
	}
	resp, err := s.executor.Execute(ctx, req)
	if err != nil {
		return "", errSegmenterDependency(err, "topics: segment: classify dispatch failed")
	}
	label := strings.TrimSpace(resp.Output)
	if label == "" {
		return "", errSegmenterEmptyLabel()
	}
	return label, nil
}

// buildClassifyRequest builds the ModelRequest one classify() call
// dispatches: task_class "classify", the §5.16 row's advisory Requirements,
// and no Policy/Sensitivity override (see this file's header comment).
func (s *segmenterImpl) buildClassifyRequest(turn Turn) (provider.ModelRequest, error) {
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

// cosineDistance returns 1-cosineSimilarity(a, b), the turn-to-turn
// distance measure spikeMap compares against HysteresisConfig.Threshold.
//
// A zero-norm vector is an ERROR, never a distance. An all-zero embedding
// carries no directional information, so any number returned for it is
// invented: returning 0 would report "these two turns are identical" and
// silently suppress every boundary at that turn, which is a wrong answer
// dressed as a confident one. embedAll's ValidBatch check catches a
// wrong-width or wrong-model vector before this; a correctly-shaped
// all-zero vector is the one malformed case that check cannot see, so it is
// refused here.
func cosineDistance(a, b []float32) (float64, error) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, cascade.New(cascade.KindInvalidInput,
			"topics: cosine distance: a zero-norm embedding has no direction to compare")
	}
	return 1 - dot/(math.Sqrt(na)*math.Sqrt(nb)), nil
}
