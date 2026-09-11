package context

import "context"

// Purpose: Compose, the assembly loop that fills a caller's ordered Slots
//   into a ComposedResult that never exceeds a declared token budget (the
//   bounded-context invariant, 05-PEWS-PLAN-W4-W6.md Wave 5 Epic U
//   S-46.T1).
// Inputs: a non-nil ctx, a positive budget, and an ordered []Slot.
// Outputs: a ComposedResult whose TokensUsed never exceeds Budget, or one
//   of composer_errors.go's typed errors.
// Constraints: DETERMINISM (this ticket's own hard rule) -- a slot either
//   fits whole, is replaced whole by a SummarizerGetter substitute that
//   fits, or is dropped whole; there is no character-level truncation
//   step, so the only sources of variation are the injected TokenCounter
//   and SummarizerGetter, both of which this package's contract requires
//   to be deterministic for a fixed input. Compose calls no model.execute
//   path directly (NOT this ticket: the rolling summarizer itself, T2).
// SPORT: context-engine/composer-core (ADD, per T-1 sport_updates).

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
				Kind: slot.Kind, Label: slot.Label, Content: slot.Content, Tokens: n0,
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
		Kind: slot.Kind, Label: slot.Label, Content: sub.Content, Tokens: n1, Summarized: true,
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
