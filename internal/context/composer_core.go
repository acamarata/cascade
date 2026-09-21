package context

import "context"

// Purpose: Compose, the assembly loop that fills a caller's ordered Slots
//   into a ComposedResult that never exceeds a declared token budget (the
//   bounded-context invariant, 05-PEWS-PLAN-W4-W6.md Wave 5 Epic U
//   S-46.T1).
// Inputs: a non-nil ctx, a positive budget, and an ordered []Slot.
// Outputs: a ComposedResult whose TokensUsed never exceeds Budget, or one
//   of composer_errors.go's typed errors.
// Constraints: DETERMINISM of the assembly loop itself -- a slot either
//   fits whole, is replaced whole by a SummarizerGetter substitute that
//   fits, or is dropped whole; there is no character-level truncation
//   step, so the only sources of variation among SURVIVING slots are the
//   injected TokenCounter and SummarizerGetter, both of which this
//   package's contract requires to be deterministic for a fixed input.
//   P1-E21-W5-S46-T3 adds the two optional PipelineStage hooks, which DO
//   call model.execute (pipeline.go) and DO transform content: an attached
//   pre-stage decides which slots the loop below even sees, and an
//   attached post-stage can replace the assembled content. So with a stage
//   attached, determinism is bounded by that stage's model, exactly as it
//   is already bounded by the SummarizerGetter's model on the overflow
//   path -- a caller that needs a byte-identical assembly across runs
//   attaches neither, which is the default. What the hooks never do is
//   turn a successful composition into a failure: a stage that cannot run
//   degrades to plain assembly plus a StageDegradedEvent.
// SPORT: context-engine/composer-core (ADD, per T-1 sport_updates;
//   pre-/post-PipelineStage hooks ADD, P1-E21-W5-S46-T3).

// Compose fills slots, in the caller's declared priority order, into a
// ComposedResult bounded by budget.
//
// Overflow semantics per slot: a slot that fits in what remains of budget
// is included whole. A slot that does not fit is offered to the
// Composer's SummarizerGetter (if any) for a compressed substitute; a
// substitute that then fits is included whole in the substitute's place.
// Any slot that still does not fit -- no summarizer configured, the
// summarizer returned an error, or its substitute still does not fit --
// is dropped whole, and a BudgetTrimEvent records the drop. Compose never
// panics and never silently emits an over-budget result.
//
// The one case Compose refuses outright, rather than returning a
// truncated-looking result: the highest-priority content slot (the first
// slot with non-empty Content, by declared order) dropping to nothing.
// Continuing past that would produce an assembly with no surviving
// authoritative content that nonetheless looks like a valid, if small,
// answer -- exactly what this ticket's invariant forbids. This mirrors
// fillTier's existing precedent in this package (assembly.go) for the
// same underlying problem.
//
// PIPELINE STAGES (P1-E21-W5-S46-T3): when the Composer has a preStage
// attached (SetPreStage, composer_config.go), Compose runs it first and
// assembles the slots it hands back -- labelled, or split into more slots
// than the caller supplied. When it has a postStage attached
// (SetPostStage), Compose runs it last over the assembled text and, if it
// returns a shorter condensation, replaces the assembled content with it
// and re-measures the token accounting so the invariant above still holds
// on what this method returns.
//
// A stage that cannot run NEVER fails the composition: Compose returns the
// un-staged assembly and publishes one StageDegradedEvent saying so
// (pipeline_compose.go). Cheap-lane capacity is a best-effort convenience,
// and a caller that asked for context must not be handed an error because
// a free lane was busy.
//
// Neither stage runs when slots is entirely empty: the ZeroSlotsEvent
// early return above already covers that case, and dispatching a stage
// over nothing to classify, segment or condense would be a wasted
// model.execute call.
func (c *Composer) Compose(ctx context.Context, budget int, slots []Slot) (ComposedResult, error) {
	if ctx == nil {
		return ComposedResult{}, errComposerNilContext()
	}
	if budget <= 0 {
		return ComposedResult{}, errComposerNonPositiveBudget(budget)
	}
	if allSlotsEmpty(slots) {
		return ComposedResult{Budget: budget, ZeroSlots: &ZeroSlotsEvent{}}, nil
	}

	staged := c.runPreStage(ctx, slots)
	result, err := c.assembleSlots(ctx, budget, staged)
	if err != nil {
		return ComposedResult{}, err
	}
	return c.runPostStage(ctx, result), nil
}

// assembleSlots runs the main per-slot fit/overflow/drop loop -- the body
// Compose itself ran before P1-E21-W5-S46-T3, split into its own method
// so Compose stays under Art.10.3's 50-line function cap once the pre-/
// post-stage hooks are added around it. Behavior is unchanged from
// before the split.
func (c *Composer) assembleSlots(ctx context.Context, budget int, slots []Slot) (ComposedResult, error) {
	result := ComposedResult{Budget: budget}
	remaining := budget

	for i, slot := range slots {
		if slot.Content == "" {
			continue
		}
		n0, err := c.counter.Count(ctx, slot.Content)
		if err != nil {
			return ComposedResult{}, errComposerDependency(err, "context: composer: counting slot content")
		}
		if n0 <= remaining {
			result.Slots = append(result.Slots, ComposedSlot{
				Kind: slot.Kind, Label: slot.Label, ClassLabel: slot.ClassLabel,
				Content: slot.Content, Tokens: n0,
			})
			result.TokensUsed += n0
			remaining -= n0
			continue
		}

		composed, event, err := c.resolveOverflow(ctx, slot, remaining)
		if err != nil {
			return ComposedResult{}, err
		}
		if composed != nil {
			result.Slots = append(result.Slots, *composed)
			result.TokensUsed += composed.Tokens
			remaining -= composed.Tokens
			continue
		}
		if len(result.Slots) == 0 {
			return ComposedResult{}, errComposerOversizedSlot(i, slot.Kind, slot.Label, n0, budget)
		}
		result.Trims = append(result.Trims, *event)
	}
	return result, nil
}

// resolveOverflow handles one slot that did not fit whole. It returns
// exactly one of: a non-nil composed slot (the summarizer's substitute
// fit), or a non-nil event describing why the slot was dropped. It never
// returns both nil, and never both non-nil.
func (c *Composer) resolveOverflow(ctx context.Context, slot Slot, remaining int) (*ComposedSlot, *BudgetTrimEvent, error) {
	if c.summarizer == nil {
		return nil, dropEvent(slot, "budget exceeded; no summarizer configured"), nil
	}
	sub, serr := c.summarizer.Summarize(ctx, slot, remaining)
	if serr != nil {
		return nil, dropEvent(slot, "summarizer returned an error; no substitute available"), nil
	}
	if sub.Content == "" {
		return nil, dropEvent(slot, "summarizer substitute was empty"), nil
	}
	n1, err := c.counter.Count(ctx, sub.Content)
	if err != nil {
		return nil, nil, errComposerDependency(err, "context: composer: counting summarizer substitute")
	}
	if n1 > remaining {
		return nil, dropEvent(slot, "summarizer substitute still exceeds remaining budget"), nil
	}
	return &ComposedSlot{
		Kind: slot.Kind, Label: slot.Label, ClassLabel: slot.ClassLabel,
		Content: sub.Content, Tokens: n1, Summarized: true,
	}, nil, nil
}

// dropEvent builds the BudgetTrimEvent for a slot dropped entirely. Dropped
// is always true: this implementation never partially includes a slot (see
// Compose's doc comment). KeptTokens is always 0 for the same reason.
func dropEvent(slot Slot, reason string) *BudgetTrimEvent {
	return &BudgetTrimEvent{
		Kind: slot.Kind, Label: slot.Label, Reason: reason, Dropped: true, KeptTokens: 0,
	}
}

// allSlotsEmpty reports whether slots has nothing to assemble: no slots at
// all, or every slot's Content is empty.
func allSlotsEmpty(slots []Slot) bool {
	for _, s := range slots {
		if s.Content != "" {
			return false
		}
	}
	return true
}
