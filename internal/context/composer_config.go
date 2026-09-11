package context

import "github.com/acamarata/cascade/pkg/provider"

// Purpose: Composer's construction: validating and holding the two
//   injected dependencies (a required TokenCounter, an optional
//   SummarizerGetter) the assembly loop (composer_core.go) consults.
// Inputs: a provider.TokenCounter, an optional SummarizerGetter.
// Outputs: *Composer, or the typed error errComposerNilCounter.
// Constraints: 02-TARGET-STRUCTURE.md §v1.1 amendments -- ctx never lives
//   in a struct; Composer carries no context.Context field, only the two
//   dependencies, both supplied once at construction.
// SPORT: context-engine/composer-config (ADD, per T-1 sport_updates).

// Composer assembles an ordered list of Slots into a ComposedResult that
// never exceeds a declared token budget (the bounded-context invariant).
// It calls no model.execute path directly: Compose is pure composition
// over caller-supplied content, measured by the injected TokenCounter and,
// on overflow, optionally compressed by the injected SummarizerGetter.
type Composer struct {
	counter    provider.TokenCounter
	summarizer SummarizerGetter
}

// NewComposer validates counter and returns a Composer wired to it and to
// the optional summarizer (nil is a valid, supported configuration: a
// Composer with no substitution seam simply trims-or-drops every
// overflowing slot).
func NewComposer(counter provider.TokenCounter, summarizer SummarizerGetter) (*Composer, error) {
	if counter == nil {
		return nil, errComposerNilCounter()
	}
	return &Composer{counter: counter, summarizer: summarizer}, nil
}
