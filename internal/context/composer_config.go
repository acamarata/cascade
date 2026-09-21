package context

import "github.com/acamarata/cascade/pkg/provider"

// Purpose: Composer's construction: validating and holding the required
//   TokenCounter dependency, and the OPTIONAL seams a caller may attach
//   after construction -- SummarizerGetter (per-slot overflow
//   substitution, T2), the two PipelineStage hooks (pre-/post-assembly
//   cheap-lane transforms, pipeline.go, P1-E21-W5-S46-T3) and the
//   StageEventPublisher those hooks degrade through -- that the assembly
//   loop (composer_core.go) consults.
// Inputs: a provider.TokenCounter, an optional SummarizerGetter, and (via
//   SetPreStage/SetPostStage/SetStageEvents) up to two optional
//   PipelineStages and one optional event publisher.
// Outputs: *Composer, or the typed error errComposerNilCounter.
// Constraints: 02-TARGET-STRUCTURE.md §v1.1 amendments -- ctx never lives
//   in a struct; Composer carries no context.Context field. SetPreStage
//   and SetPostStage are ordinary methods, not constructor arguments, so
//   NewComposer's two-argument signature -- and every existing caller and
//   test that already depends on it -- stays unchanged; a caller that
//   wants pipeline stages opts in explicitly, after construction, exactly
//   as a caller that wants no SummarizerGetter already passes nil for it.
// SPORT: context-engine/composer-config (ADD, per T-1 sport_updates;
//   SetPreStage/SetPostStage ADD, P1-E21-W5-S46-T3).

// Composer assembles an ordered list of Slots into a ComposedResult that
// never exceeds a declared token budget (the bounded-context invariant).
// Compose is composition over caller-supplied content, measured by the
// injected TokenCounter and, on overflow, optionally compressed by the
// injected SummarizerGetter. It optionally also runs an injected
// PipelineStage before and after that composition pass -- see
// composer_core.go's Compose doc comment for what those two hooks
// transform and what happens when one cannot run.
type Composer struct {
	counter    provider.TokenCounter
	summarizer SummarizerGetter
	preStage   PipelineStage
	postStage  PipelineStage
	events     StageEventPublisher
}

// NewComposer validates counter and returns a Composer wired to it and to
// the optional summarizer (nil is a valid, supported configuration: a
// Composer with no substitution seam simply trims-or-drops every
// overflowing slot). preStage and postStage start nil (disabled); attach
// them with SetPreStage/SetPostStage below.
func NewComposer(counter provider.TokenCounter, summarizer SummarizerGetter) (*Composer, error) {
	if counter == nil {
		return nil, errComposerNilCounter()
	}
	return &Composer{counter: counter, summarizer: summarizer}, nil
}

// SetPreStage installs stage as the pre-assembly PipelineStage Compose
// runs exactly once, before its assembly loop begins, over the caller's
// original slots -- which the stage may LABEL or SPLIT, and the assembly
// loop then works on whatever it handed back. Passing nil disables the
// pre-assembly step: the default after NewComposer, and the same
// nil-is-supported convention the summarizer argument above already
// documents. pipeline.go's NewPreStage is this package's pre-assembly
// constructor; it pins the §5.16 task class matching the kind it is given,
// so a stage built through this package's API cannot declare the wrong
// one.
func (c *Composer) SetPreStage(stage PipelineStage) {
	c.preStage = stage
}

// SetPostStage installs stage as the post-assembly PipelineStage Compose
// runs exactly once, after its assembly loop completes, over the assembled
// text -- which the stage may CONDENSE, in which case Compose re-measures
// the result so the bounded-context invariant still holds on what it
// returns. See SetPreStage's doc for the nil convention; pipeline.go's
// NewSummarizeStage is this package's post-assembly constructor.
func (c *Composer) SetPostStage(stage PipelineStage) {
	c.postStage = stage
}

// SetStageEvents installs the publisher Compose reports StageDegradedEvents
// through when an attached stage does not apply. Passing nil (the default
// after NewComposer) drops every such event; it never changes what Compose
// returns, matching NewSummarizer's identical stance for its own events
// argument.
func (c *Composer) SetStageEvents(events StageEventPublisher) {
	c.events = events
}
