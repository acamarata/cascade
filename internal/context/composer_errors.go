package context

import "github.com/acamarata/cascade/pkg/cascade"

// Purpose: the typed-error constructors for the Composer and for the
//   PipelineStage implementations it runs (pipeline.go). Every message here
//   is built from fixed strings, slot Kind/Label, indices and counts --
//   never Slot.Content and never model output -- so an error path can never
//   echo conversation content (this ticket's privacy hard rule).
// Inputs: none.
// Outputs: *cascade.Error values, always KindInvalidInput (caller-supplied
//   misuse) except errComposerDependency, errPipelineDependency and
//   errPipelineMeasure, which preserve whatever Kind an injected
//   TokenCounter, SummarizerGetter or ModelExecutor failure already
//   carries.
// Constraints: 12-QUALITY-CONSTITUTION.md Art.1/Art.3 (typed errors only,
//   frozen 14-kind taxonomy, never a bare fmt.Errorf at this boundary).
//   There is deliberately NO composer-level "pipeline stage failed" error:
//   a stage failure degrades to plain assembly and publishes an event
//   (pipeline_compose.go), so Compose has nothing to wrap.
// SPORT: context-engine/composer-errors (ADD, per T-1 sport_updates;
//   pipeline-stage errors ADD, P1-E21-W5-S46-T3).

// errComposerNilCounter reports that NewComposer was given a nil
// TokenCounter. A Composer with no way to measure content cannot enforce
// the bounded-context invariant at all, so this refuses at construction
// rather than deferring to the first Compose call.
func errComposerNilCounter() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: composer: TokenCounter must not be nil")
}

// errComposerNilContext reports a nil ctx passed to Compose.
func errComposerNilContext() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: composer: ctx must not be nil")
}

// errComposerNonPositiveBudget reports budget <= 0. Per this ticket's
// overflow semantics, a non-positive budget is a refusal, never a silent
// no-op and never an empty-but-valid result.
func errComposerNonPositiveBudget(budget int) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: composer: budget must be positive, got %d", budget)
}

// errComposerOversizedSlot reports that a slot's measured token count
// exceeds budget by itself -- no ordering of the other slots could ever
// make it fit. Reports Kind, Label and the index only; never Content.
func errComposerOversizedSlot(index int, kind SlotKind, label string, tokens, budget int) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: composer: slot %d (%s %q) needs %d tokens, exceeding the entire budget of %d",
		index, kind, label, tokens, budget)
}

// errComposerDependency wraps a TokenCounter or SummarizerGetter failure as
// a taxonomy error, preserving its Kind when it already carries one
// (matching assembly.go's wrapDependencyErr precedent in this package)
// rather than collapsing every dependency failure to one kind.
func errComposerDependency(err error, msg string) error {
	if k, ok := cascade.KindOf(err); ok {
		return cascade.Wrap(k, err, msg)
	}
	return cascade.Wrap(cascade.KindInternal, err, msg)
}

// errPipelineNilExecutor reports that NewPreStage or NewSummarizeStage was
// given a nil provider.ModelExecutor: a stage with nothing to dispatch
// through cannot honor its own TaskClass contract, so this refuses at
// construction (matching NewComposer's errComposerNilCounter precedent).
func errPipelineNilExecutor(taskClass string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: pipeline: %s stage: ModelExecutor must not be nil", taskClass)
}

// errPipelineNilCounter reports NewSummarizeStage called without a
// TokenCounter. The summarize stage's whole contract is that it replaces the
// assembly only with something SMALLER, and it cannot establish that
// without measuring.
func errPipelineNilCounter(taskClass string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: pipeline: %s stage: TokenCounter must not be nil", taskClass)
}

// errPipelineUnknownPreStageKind reports NewPreStage called with a kind
// outside the declared two. Failing closed matters here: defaulting to
// classify would silently give a caller that asked for segmentation a
// labelling pass instead.
func errPipelineUnknownPreStageKind(kind PreStageKind) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: pipeline: %q is not a pre-stage kind", kind.String())
}

// errPipelineNilInput reports Execute called with a nil StageInput. The
// composer always passes one; a nil means a caller wired a stage up by hand
// and has nowhere for the transform to write.
func errPipelineNilInput(taskClass string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: pipeline: %s stage: StageInput must not be nil", taskClass)
}

// errPipelineEmptyInput reports a stage asked to run over nothing: there is
// nothing to classify, segment or condense, and dispatching an empty prompt
// would be a wasted model.execute call rather than a refusal.
func errPipelineEmptyInput(taskClass string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: pipeline: %s stage: input must not be empty", taskClass)
}

// errPipelineDependency wraps a model.execute failure, preserving its own
// Kind exactly as errComposerDependency does for the composer's own
// dependencies.
func errPipelineDependency(err error, taskClass string) error {
	return errComposerDependency(err,
		"context: pipeline: "+taskClass+" stage: model.execute dispatch failed")
}

// errPipelineMeasure wraps a TokenCounter failure inside the summarize
// transform, which cannot decide whether a condensation is smaller without
// one.
func errPipelineMeasure(err error) error {
	return errComposerDependency(err,
		"context: pipeline: summarize stage: measuring the condensation")
}
