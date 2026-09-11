package context

import "github.com/acamarata/cascade/pkg/cascade"

// Purpose: the Composer's typed-error constructors. Every message here is
//   built from fixed strings, slot Kind/Label, indices and counts --
//   never Slot.Content -- so an error path can never echo conversation
//   content (this ticket's privacy hard rule).
// Inputs: none.
// Outputs: *cascade.Error values, always KindInvalidInput (caller-supplied
//   misuse) except errComposerDependency, which preserves whatever Kind an
//   injected TokenCounter or SummarizerGetter failure already carries.
// Constraints: 12-QUALITY-CONSTITUTION.md Art.1/Art.3 (typed errors only,
//   frozen 14-kind taxonomy, never a bare fmt.Errorf at this boundary).
// SPORT: context-engine/composer-errors (ADD, per T-1 sport_updates).

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
