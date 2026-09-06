package context

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: Assemble is the central pipeline that merges instruction-tier
//   content (MergedContext, S-08), RRF-fused retrieved chunks (F/S-11) and
//   an optional memory window (provider.MemoryReader) into one
//   provider.ContextAssembly that satisfies a declared token budget
//   (R-14.97) without exceeding it and without silently dropping content
//   the caller cannot then account for.
// Inputs: an AssembleInput: a REQUIRED scope.SessionScope (R-16.4), a
//   MergedContext, the fused ranking, a citations.SourceResolver that
//   decides which fused chunks the session may see, an optional
//   MemoryReader, a REQUIRED TokenCounter, and a BudgetConfig.
// Outputs: a provider.ContextAssembly whose SlotCounts always sum to at
//   most the budget, and whose Dropped list names every unit of content a
//   slot's tail truncation removed — or a typed cascade.Error for a
//   malformed input, a zero/invalid budget, or a highest-tier block that
//   cannot fit at all.
// Constraints: no bare time.Now (nothing here needs a clock); the retrieval
//   slot's "content" is each surviving chunk's rendered citation line
//   (internal/retrieval/citations), not the chunk's indexed body text —
//   neither rrf.FusedResult nor corpus.Record carries body text, only
//   provenance, so citation text is the only real content available to
//   token-count at this boundary. A ScopeKindGeneral session (R-16.3: an
//   unresolved cwd's restricted result) never receives retrieval content,
//   whatever the resolver would otherwise authorize.
// SPORT: context-engine/assembly (ADD, per T-1 sport_updates).

// AssembleInput carries every input to Assemble.
type AssembleInput struct {
	// Scope is the asking session's resolved scope (R-16.4). REQUIRED:
	// Assemble refuses an invalid (zero-value) Kind.
	Scope scope.SessionScope
	// Merged is the S-08 instruction-tier merge result. REQUIRED: a nil
	// Merged is a typed error, never an empty assembly.
	Merged *MergedContext
	// Ranked is the RRF-fused candidate list, best result first. May be
	// nil or empty: that yields an empty retrieval slot, never an error.
	Ranked []rrf.FusedResult
	// Resolver decides which of Ranked's chunks this session may see
	// (R-16.4: own scope chain plus declared edge targets, resolved by the
	// caller into this same seam citations.Assemble already uses). Nil is
	// only tolerated when Ranked has nothing to authorize.
	Resolver citations.SourceResolver
	// Reader supplies the optional memory window. Nil means no memory
	// source; the memory slot is then empty, not an error.
	Reader provider.MemoryReader
	// Counter measures token cost. REQUIRED: a nil Counter is a typed
	// error.
	Counter provider.TokenCounter
	// Budget is the token-budget allocation input.
	Budget provider.BudgetConfig
}

// Assemble builds a provider.ContextAssembly from in.
func Assemble(ctx context.Context, in AssembleInput) (provider.ContextAssembly, error) {
	if in.Merged == nil {
		return provider.ContextAssembly{}, cascade.New(cascade.KindInvalidInput,
			"context: assemble: MergedContext is nil")
	}
	if in.Counter == nil {
		return provider.ContextAssembly{}, cascade.New(cascade.KindInvalidInput,
			"context: assemble: TokenCounter is nil")
	}
	if !in.Scope.Kind.Valid() {
		return provider.ContextAssembly{}, cascade.New(cascade.KindInvalidInput,
			"context: assemble: SessionScope has an invalid kind")
	}

	alloc, err := NewBudgetAllocator(in.Budget)
	if err != nil {
		return provider.ContextAssembly{}, err
	}
	sizes := alloc.Allocate()

	tierBlocks, tierTokens, tierDrops, err := fillTier(ctx, in.Merged.Sections, in.Counter, sizes.Tier)
	if err != nil {
		return provider.ContextAssembly{}, err
	}
	chunks, chunkTokens, chunkDrops, err := fillRetrieval(ctx, in.Scope, in.Ranked, in.Resolver, in.Counter, sizes.Retrieval)
	if err != nil {
		return provider.ContextAssembly{}, err
	}
	memItems, memTokens, memDrops, err := fillMemory(ctx, in.Reader, in.Counter, sizes.Memory)
	if err != nil {
		return provider.ContextAssembly{}, err
	}

	var dropped []provider.DroppedItem
	dropped = append(dropped, tierDrops...)
	dropped = append(dropped, chunkDrops...)
	dropped = append(dropped, memDrops...)

	return provider.ContextAssembly{
		Tier:      tierBlocks,
		Retrieval: chunks,
		Memory:    memItems,
		Counts: provider.SlotCounts{
			Tier: tierTokens, Retrieval: chunkTokens, Memory: memTokens,
			Total: tierTokens + chunkTokens + memTokens,
		},
		Dropped: dropped,
	}, nil
}

// fillTier walks sections in the order MergeTiers emits them (ascending
// ordinal, most-authoritative first) and includes as many as fit within
// budget, tail-truncating the rest into dropped. The first section alone
// exceeding budget is a distinct, typed failure: silently truncating a
// caller's only authoritative content to nothing would be indistinguishable
// from an empty merge.
func fillTier(ctx context.Context, sections []MergedSection, counter provider.TokenCounter, budget int) ([]provider.TierBlock, int, []provider.DroppedItem, error) {
	var blocks []provider.TierBlock
	var dropped []provider.DroppedItem
	used := 0
	for i, sec := range sections {
		n, cerr := counter.Count(ctx, sec.Content)
		if cerr != nil {
			return nil, 0, nil, wrapDependencyErr(cerr, "context: assemble: counting tier content")
		}
		if used+n > budget {
			if i == 0 {
				return nil, 0, nil, cascade.Newf(cascade.KindInvalidInput,
					"context: assemble: budget %d is too small for the highest-tier block (%d tokens)", budget, n)
			}
			for _, rest := range sections[i:] {
				dropped = append(dropped, provider.DroppedItem{
					Slot: provider.SlotTier, Detail: dropDetail(rest.Role.String(), rest.Heading),
					Reason: "tier budget exceeded",
				})
			}
			break
		}
		used += n
		blocks = append(blocks, provider.TierBlock{
			Heading: sec.Heading, Content: sec.Content, Role: sec.Role.String(), Tokens: n,
		})
	}
	return blocks, used, dropped, nil
}

// fillRetrieval authorizes ranked against resolver (R-16.4) BEFORE any slot
// population, using the same citation-assembly seam recall/query.go already
// consults: a fused result the resolver does not authorize contributes
// nothing here, and — deliberately — is never reported in dropped either,
// because naming a withheld chunk's id or path in a "here is what did not
// fit" list would itself disclose content the session was not cleared to
// see. Only budget-driven tail truncation, applied after authorization,
// appears in dropped. A ScopeKindGeneral session (R-16.3's restricted
// result for an unresolved cwd) never receives retrieval content at all.
func fillRetrieval(
	ctx context.Context, sc scope.SessionScope, ranked []rrf.FusedResult,
	resolver citations.SourceResolver, counter provider.TokenCounter, budget int,
) ([]provider.RetrievedChunk, int, []provider.DroppedItem, error) {
	if sc.Kind == scope.ScopeKindGeneral || len(ranked) == 0 {
		return nil, 0, nil, nil
	}
	if resolver == nil {
		return nil, 0, nil, cascade.New(cascade.KindInvalidInput,
			"context: assemble: retrieval results require a non-nil scope resolver (R-16.4)")
	}
	set, err := citations.Assemble(ranked, citations.Options{Resolver: resolver})
	if err != nil {
		return nil, 0, nil, err
	}
	lines := citationLines(set)

	var chunks []provider.RetrievedChunk
	var dropped []provider.DroppedItem
	used := 0
	for i, c := range set.Citations {
		text := ""
		if i < len(lines) {
			text = lines[i]
		}
		n, cerr := counter.Count(ctx, text)
		if cerr != nil {
			return nil, 0, nil, wrapDependencyErr(cerr, "context: assemble: counting retrieval content")
		}
		if used+n > budget {
			for _, rest := range set.Citations[i:] {
				dropped = append(dropped, provider.DroppedItem{
					Slot: provider.SlotRetrieval, Detail: rest.ChunkID, Reason: "retrieval budget exceeded",
				})
			}
			break
		}
		used += n
		chunks = append(chunks, provider.RetrievedChunk{
			ChunkID: c.ChunkID, Path: c.Path, CorpusID: c.CorpusID, Trust: c.Trust.String(),
			Score: c.Score, Strategies: stratStrings(c.Strategies), Tokens: n,
		})
	}
	return chunks, used, dropped, nil
}

// citationLines renders set once and splits its definitions block back into
// one line per citation, in the same order as set.Citations, reusing
// citations.Render rather than duplicating its rendering rules here.
func citationLines(set citations.CitationSet) []string {
	rendered := citations.Render(set)
	if rendered.Definitions == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(rendered.Definitions, "\n"), "\n")
}

// fillMemory calls reader.Fetch (nil reader: empty slot, no error) and
// re-measures every returned item with counter, tail-truncating whatever
// does not fit — Assemble never trusts that Fetch itself honored budget.
func fillMemory(ctx context.Context, reader provider.MemoryReader, counter provider.TokenCounter, budget int) ([]provider.MemoryItem, int, []provider.DroppedItem, error) {
	if reader == nil {
		return nil, 0, nil, nil
	}
	items, err := reader.Fetch(ctx, budget)
	if err != nil {
		return nil, 0, nil, wrapDependencyErr(err, "context: assemble: memory reader fetch failed")
	}
	var out []provider.MemoryItem
	var dropped []provider.DroppedItem
	used := 0
	for i, m := range items {
		n, cerr := counter.Count(ctx, m.Content)
		if cerr != nil {
			return nil, 0, nil, wrapDependencyErr(cerr, "context: assemble: counting memory content")
		}
		if used+n > budget {
			for range items[i:] {
				dropped = append(dropped, provider.DroppedItem{
					Slot: provider.SlotMemory, Detail: "memory item", Reason: "memory budget exceeded",
				})
			}
			break
		}
		used += n
		out = append(out, provider.MemoryItem{Content: m.Content, Tokens: n})
	}
	return out, used, dropped, nil
}

// dropDetail names a tail-truncated tier section for DroppedItem.Detail.
func dropDetail(role, heading string) string {
	if heading == "" {
		return role + " preamble"
	}
	return role + ": " + heading
}

// stratStrings renders a citation's contributing strategies as plain
// strings, the pkg/provider-safe form of rrf.StrategyName.
func stratStrings(in []rrf.StrategyName) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

// wrapDependencyErr wraps an injected dependency's failure (MemoryReader,
// TokenCounter) as a taxonomy error, preserving its Kind when it already
// carries one rather than collapsing every dependency failure to one kind.
func wrapDependencyErr(err error, msg string) error {
	if k, ok := cascade.KindOf(err); ok {
		return cascade.Wrap(k, err, msg)
	}
	return cascade.Wrap(cascade.KindInternal, err, msg)
}
