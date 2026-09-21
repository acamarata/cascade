package context

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the PipelineStage implementations this package ships (the
//   interface itself lives in composer_types.go): a pre-assembly stage in
//   one of two kinds -- CLASSIFY, which labels every slot, and SEGMENT,
//   which splits over-long slots at model-chosen boundaries -- and a
//   post-assembly SUMMARIZE stage, which condenses the assembled context.
//   Each dispatches exactly one model.execute call declaring one of the
//   cheap-end 06-FORGE-SPEC.md §5.16 task classes, so pipeline overhead
//   never competes with the caller's own task for expensive-lane capacity.
// Inputs: a non-nil provider.ModelExecutor at construction (plus a
//   provider.TokenCounter for the summarize stage, which must measure
//   whether a condensation is actually shorter); a StageInput with content
//   in the half its own position uses.
// Outputs: the transformed StageInput (labels, split slots, condensed
//   text), or a typed error when the stage could not run at all.
// Constraints: model.execute is the ONLY model door (07-CLI-COMMAND-TREE.md
//   §run). Production code in this package NEVER imports internal/conductor
//   (summarizer_dispatch.go sets this precedent), so every task-class
//   constant below is the literal §5.16 name, pinned against
//   conductor.TaskClassClassify/Segment/Summarize by a test rather than by
//   an import.
//
//   A STAGE TRANSFORMS, IT DOES NOT GATE. An earlier revision returned
//   success or failure only and folded no model output back into the
//   composition, which made every one of these dispatches a model call
//   whose result was discarded. What each stage now writes back is
//   described per kind in pipeline_transform.go, and what the composer does
//   with it in pipeline_compose.go. A stage that cannot run never fails the
//   composition.
//
//   SENSITIVITY IS INHERITED, NOT DECLARED HERE, matching
//   summarizer_dispatch.go's identical stance: every request below sets NO
//   Policy and leaves Sensitivity at its zero value (which resolves to
//   provider.SensitivityRestricted, R-21.264). The conductor's FILTER 0
//   applies the calling thread's privacy_mode regardless of what the
//   request itself says, so this caller's only obligation is never to
//   loosen the inherited mode -- which an absent Policy and an unset
//   Sensitivity together guarantee. RequiredCapabilities is also left at
//   its zero value: a cheap/free lane need not advertise any capability
//   dimension, and over-declaring one would only risk excluding a lane
//   that would otherwise qualify.
//
//   No injected Clock: TaskID uniqueness comes from cascade.NewID's
//   crypto-random suffix (summarizer_dispatch.go's own precedent for the
//   identical need), so this file never reads the wall clock at all.
// SPORT: context-engine/pipeline (ADD, P1-E21-W5-S46-T3).

// The three §5.16 task-class names this file pins to, transcribed verbatim
// from 06-FORGE-SPEC.md §5.16. classify and segment are the pre-assembly
// pair (LaneAffinity "cheapest/free"); summarize is the post-assembly class
// (LaneAffinity "cheap") -- the same class name T2's rolling summarizer
// already uses (summarizeTaskClass, summarizer_dispatch.go) for its own,
// unrelated, per-slot overflow substitute. The two engines share the name
// because both rows share it in §5.16; they share no code and no
// compile-time dependency.
const (
	pipelineTaskClassClassify  = "classify"
	pipelineTaskClassSegment   = "segment"
	pipelineTaskClassSummarize = "summarize"
)

// The matching §5.16 Requirements row for each class: Reasoning, context
// window in tokens (the row's CtxK * 1000) and Structured. Advisory only --
// summarizer_dispatch.go's header note applies here too: filterCapability
// gates lanes on RequiredCapabilities, never on Requirements, so these
// values can never exclude a lane. A test asserts each triple against the
// taxonomy row it transcribes, so a drifted value is a red test.
const (
	pipelineClassifyReasoning = "low"
	pipelineClassifyContext   = 8000

	pipelineSegmentReasoning = "low"
	pipelineSegmentContext   = 16000

	pipelineSummarizeReasoning = "low"
	pipelineSummarizeContext   = 32000
)

// PreStageKind names which pre-assembly transform a stage performs. There
// is one constructor for both kinds (NewPreStage) rather than one per kind:
// a caller attaches exactly one pre-assembly stage, and making the choice a
// parameter is what keeps the second kind reachable from production code
// instead of shipping as an unused alternative.
type PreStageKind uint8

const (
	_ PreStageKind = iota // 0 is deliberately not a valid kind

	// PreStageClassify labels every slot with one category word before
	// assembly, declaring §5.16 task class "classify".
	PreStageClassify
	// PreStageSegment splits every slot longer than its share of the
	// input at model-chosen boundaries before assembly, declaring §5.16
	// task class "segment".
	PreStageSegment
)

// String returns the kind's stable lowercase name, for error messages.
func (k PreStageKind) String() string {
	switch k {
	case PreStageClassify:
		return pipelineTaskClassClassify
	case PreStageSegment:
		return pipelineTaskClassSegment
	default:
		return "invalid-pre-stage-kind"
	}
}

// stageRender builds the prompt one stage dispatches from its working set,
// and owns that half's empty-input guard.
type stageRender func(in *StageInput) (string, error)

// stageTransform folds one model response back into the working set. It
// returns a fixed rejection reason (StageInput.Degraded) when the response
// was unusable and the working set was therefore left untouched, or an
// error only when a dependency the transform itself needs failed.
type stageTransform func(ctx context.Context, in *StageInput, output string, counter provider.TokenCounter) (string, error)

// modelPipelineStage is PipelineStage's production implementation: one
// fixed §5.16 class and Requirements row, the render/transform pair that
// implements its kind, and the seams it dispatches and measures through.
// Unexported: callers reach it only through NewPreStage and
// NewSummarizeStage, which pin class, requirements and transform together
// so the three can never drift apart.
type modelPipelineStage struct {
	taskClass  string
	reasoning  string
	ctxTokens  int
	structured bool
	render     stageRender
	transform  stageTransform
	executor   provider.ModelExecutor
	counter    provider.TokenCounter
}

var _ PipelineStage = (*modelPipelineStage)(nil)

// NewPreStage returns the pre-assembly PipelineStage for kind: a classify
// stage (§5.16 task class "classify") or a segment stage (task class
// "segment"), both with LaneAffinity "cheapest/free". An unknown kind is
// refused rather than defaulted, so a caller that forgot to set one gets an
// error instead of a silently-chosen transform.
func NewPreStage(kind PreStageKind, executor provider.ModelExecutor) (PipelineStage, error) {
	switch kind {
	case PreStageClassify:
		return newModelPipelineStage(&modelPipelineStage{
			taskClass: pipelineTaskClassClassify, reasoning: pipelineClassifyReasoning,
			ctxTokens: pipelineClassifyContext, structured: true,
			render: renderClassifyPrompt, transform: applyClassify, executor: executor,
		})
	case PreStageSegment:
		return newModelPipelineStage(&modelPipelineStage{
			taskClass: pipelineTaskClassSegment, reasoning: pipelineSegmentReasoning,
			ctxTokens: pipelineSegmentContext, structured: true,
			render: renderSegmentPrompt, transform: applySegment, executor: executor,
		})
	default:
		return nil, errPipelineUnknownPreStageKind(kind)
	}
}

// NewSummarizeStage returns the post-assembly PipelineStage: it declares
// §5.16 task class "summarize" (LaneAffinity "cheap") and condenses the
// FINAL assembled context. It is unrelated to, and never calls, T2's
// SummarizerGetter (summarizer_dispatch.go), which substitutes for one
// oversized slot DURING assembly rather than condensing the result.
//
// counter is required and is not the composer's: this stage decides for
// itself whether the model's answer is actually shorter than what it would
// replace, and a stage that cannot measure cannot make that call. The
// composer re-measures with its own counter before accepting the result
// (applyCondensed, pipeline_compose.go), so the two measurements are
// independent by design.
func NewSummarizeStage(executor provider.ModelExecutor, counter provider.TokenCounter) (PipelineStage, error) {
	if counter == nil {
		return nil, errPipelineNilCounter(pipelineTaskClassSummarize)
	}
	return newModelPipelineStage(&modelPipelineStage{
		taskClass: pipelineTaskClassSummarize, reasoning: pipelineSummarizeReasoning,
		ctxTokens: pipelineSummarizeContext, structured: false,
		render: renderSummarizePrompt, transform: applySummarize,
		executor: executor, counter: counter,
	})
}

// newModelPipelineStage validates the one dependency every stage needs.
func newModelPipelineStage(s *modelPipelineStage) (PipelineStage, error) {
	if s.executor == nil {
		return nil, errPipelineNilExecutor(s.taskClass)
	}
	return s, nil
}

// TaskClass implements PipelineStage.
func (s *modelPipelineStage) TaskClass() string { return s.taskClass }

// Execute implements PipelineStage: render the prompt from the working set,
// dispatch exactly one model.execute call under this stage's declared task
// class, and fold the response back into the working set.
func (s *modelPipelineStage) Execute(ctx context.Context, in *StageInput) error {
	if in == nil {
		return errPipelineNilInput(s.taskClass)
	}
	prompt, err := s.render(in)
	if err != nil {
		return err
	}
	req, err := s.request(prompt)
	if err != nil {
		return err
	}
	resp, err := s.executor.Execute(ctx, req)
	if err != nil {
		return errPipelineDependency(err, s.taskClass)
	}
	reason, err := s.transform(ctx, in, resp.Output, s.counter)
	if err != nil {
		return err
	}
	in.Degraded = reason
	return nil
}

// request builds the ModelRequest this stage dispatches. Split out of
// Execute so a lane test can construct the exact request a stage would send
// without a live executor (mirroring summarizer_dispatch.go's
// buildModelRequest / summarizer_cheap_lane_test.go split). Sets NO Policy
// and leaves Sensitivity and RequiredCapabilities at their zero values --
// see the file Purpose comment's "sensitivity is inherited" note.
func (s *modelPipelineStage) request(prompt string) (provider.ModelRequest, error) {
	id, err := cascade.NewID()
	if err != nil {
		return provider.ModelRequest{}, cascade.Wrap(cascade.KindInternal, err,
			"context: pipeline: minting task id")
	}
	return provider.ModelRequest{
		TaskID:    "pipeline-" + s.taskClass + "-" + string(id),
		TaskClass: s.taskClass,
		Inputs:    []provider.ChatMessage{{Role: "user", Content: prompt}},
		Requirements: provider.Requirements{
			Reasoning:  s.reasoning,
			Context:    s.ctxTokens,
			Structured: s.structured,
		},
	}, nil
}
