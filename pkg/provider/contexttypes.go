// Purpose: the exported data types internal/context.Assemble and its
//   callers exchange across the pkg/provider boundary: the resolved
//   ContextAssembly, its BudgetConfig input, and the per-slot accounting
//   and drop reporting that let a caller tell a complete assembly from a
//   truncated one apart, rather than discovering the gap by its absence.
// Inputs: none — type definitions, plus BudgetConfig.Validate's own checks.
// Outputs: BudgetConfig.Validate returns a typed cascade.Error for an
//   unusable configuration; every other type here is a plain data carrier.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2), so
//   every field here is expressed in stdlib-only types. ContextAssembly
//   never carries an internal/retrieval or internal/context/scope type
//   directly — Assemble translates (TrustLevel/StrategyName to string,
//   TierRole to its String()) on the way out, once, at the boundary.
// SPORT: pkg.provider.ContextAssembly,BudgetConfig/ADDED (P1-E05-W2-S09-T1).

package provider

import "github.com/acamarata/cascade/pkg/cascade"

// SlotKind names one of the three content slots a ContextAssembly fills.
// Every DropReason is reported alongside the SlotKind it belongs to, so a
// caller can tell which budget was exceeded.
type SlotKind string

const (
	// SlotTier is the instruction-tier slot (GCI..PAI content).
	SlotTier SlotKind = "tier"
	// SlotRetrieval is the retrieval slot (fused, cited chunks).
	SlotRetrieval SlotKind = "retrieval"
	// SlotMemory is the memory slot (MemoryReader output).
	SlotMemory SlotKind = "memory"
)

// TierBlock is one surviving instruction-tier section, flattened to the
// fields a caller needs to render it. Role is the tier's short display name
// (e.g. "GCI", "PAI") rather than a typed TierRole, since pkg/provider
// cannot import internal/context.
type TierBlock struct {
	Heading string
	Content string
	Role    string
	Tokens  int
}

// RetrievedChunk is one retrieval-slot entry, flattened from the fused
// ranking's citation. Trust and Strategies are rendered as strings rather
// than internal/retrieval/corpus.TrustLevel and rrf.StrategyName, for the
// same import-boundary reason as TierBlock.Role.
type RetrievedChunk struct {
	ChunkID    string
	Path       string
	CorpusID   string
	Trust      string
	Score      float64
	Strategies []string
	Tokens     int
}

// MemoryItem is one memory-slot entry: either what a MemoryReader.Fetch
// returns (Tokens unset, filled in by Assemble) or what ContextAssembly.Memory
// reports back (Tokens set to the measured count).
type MemoryItem struct {
	Content string
	Tokens  int
}

// SlotCounts is the per-slot token accounting for one ContextAssembly.
// Total always equals Tier+Retrieval+Memory, and Total never exceeds the
// BudgetConfig.MaxTokens that produced it: Assemble enforces this before
// ever returning a value.
type SlotCounts struct {
	Tier      int
	Retrieval int
	Memory    int
	Total     int
}

// DroppedItem records one unit of content that did not survive the budget
// trim, so a caller can tell a complete assembly from a truncated one
// instead of discovering the gap by its absence. Content withheld for scope
// authorization reasons is never reported here (that would itself be a
// disclosure); only budget-driven drops are.
type DroppedItem struct {
	Slot   SlotKind
	Detail string
	Reason string
}

// ContextAssembly is the resolved, token-budgeted context for one request:
// every slot's surviving content, its token accounting, and a record of
// whatever did not fit. See ExampleContextAssembly for a runnable
// demonstration of reading one.
type ContextAssembly struct {
	// Tier holds the surviving instruction-tier blocks, most-authoritative
	// first (GCI before PAI).
	Tier []TierBlock
	// Retrieval holds the surviving retrieval-slot chunks, best-ranked
	// first.
	Retrieval []RetrievedChunk
	// Memory holds the surviving memory-slot items.
	Memory []MemoryItem
	// Counts is the resolved per-slot token accounting.
	Counts SlotCounts
	// Dropped records, in drop order, every unit of budget-driven content
	// loss across every slot.
	Dropped []DroppedItem
}

// BudgetConfig is the token-budget allocation input to Assemble.
//
// MaxTokens bounds the whole assembly. TierRatio, RetrievalRatio and
// MemoryRatio are each expected in [0,1] and, together with BufferRatio,
// must sum to at most 1.0; whatever is left over (1.0 minus their sum) is
// added to the tier slot, per R-14.97 — the tier slot, carrying the
// highest-authority instructions, is never the one starved by an under-
// specified config. BufferRatio reserves a portion of MaxTokens that no
// slot may fill (headroom for the caller's own framing around the
// assembly); it counts against the 1.0 ceiling like the other three but
// carries no residual role of its own.
type BudgetConfig struct {
	MaxTokens      int
	TierRatio      float64
	RetrievalRatio float64
	MemoryRatio    float64
	BufferRatio    float64
}

// budgetRatio names one of BudgetConfig's four ratio fields for Validate's
// ordered error reporting; a map would make which violation is reported
// first depend on Go's randomized iteration order.
type budgetRatio struct {
	name  string
	value float64
}

// Validate checks the invariants Assemble depends on: MaxTokens positive,
// every ratio non-negative, and the four ratios summing to at most 1.0. It
// does not check MaxTokens against any real content — that is the
// highest-tier-block check Assemble itself performs once real content is
// known.
func (c BudgetConfig) Validate() error {
	if c.MaxTokens <= 0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: budget max_tokens must be positive, got %d", c.MaxTokens)
	}
	ratios := [...]budgetRatio{
		{"tier_ratio", c.TierRatio},
		{"retrieval_ratio", c.RetrievalRatio},
		{"memory_ratio", c.MemoryRatio},
		{"buffer_ratio", c.BufferRatio},
	}
	sum := 0.0
	for _, r := range ratios {
		if r.value < 0 {
			return cascade.Newf(cascade.KindInvalidInput,
				"provider: budget %s must not be negative, got %v", r.name, r.value)
		}
		sum += r.value
	}
	if sum > 1.0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: budget ratios must sum to at most 1.0, got %v", sum)
	}
	return nil
}
