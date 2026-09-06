// Purpose: the two exported seams internal/context.Assemble consumes from
//   outside pkg/provider's own boundary: MemoryReader (an optional memory-
//   window source) and TokenCounter (how Assemble measures how much of a
//   token budget a piece of content spends). Both live here, rather than in
//   internal/context, so a third-party implementation of either never needs
//   to import internal/.
// Inputs: none — interface declarations only.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); this
//   file declares contracts only. The one implementation that ships from
//   this ticket, the documented naive TokenCounter fallback, lives in
//   tokencounter.go instead of here.
// SPORT: pkg.provider.MemoryReader,TokenCounter/ADDED (P1-E05-W2-S09-T1).

package provider

import "context"

// MemoryReader supplies the optional memory window a ContextAssembly's
// memory slot fills from.
//
// A nil MemoryReader is not an error: Assemble treats it as "this caller
// has no memory source" and produces a ContextAssembly with an empty memory
// slot, zero memory tokens, and no error.
type MemoryReader interface {
	// Fetch returns memory items to offer the memory slot, best-effort
	// sized to budget tokens. Assemble does not trust that budget was
	// honored: it re-measures every returned item with its own
	// TokenCounter and tail-truncates whatever does not fit, so an
	// implementation that over-returns is corrected rather than silently
	// accepted.
	//
	// An empty result and a nil error is a legitimate "nothing relevant
	// right now", not a miss. A non-nil error means the memory source
	// itself failed; Assemble propagates it as a pkg/cascade taxonomy
	// error.
	Fetch(ctx context.Context, budget int) ([]MemoryItem, error)
}

// TokenCounter measures how many tokens a piece of text spends against a
// budget. Different model families tokenize differently, so Assemble takes
// this as an injected dependency rather than assuming one tokenizer.
type TokenCounter interface {
	// Count returns text's token count under this counter's tokenizer. A
	// non-nil error means the counter itself failed (for example, a
	// remote tokenizer service was unreachable); on error the returned
	// count is always 0 and must not be used.
	Count(ctx context.Context, text string) (int, error)
}
