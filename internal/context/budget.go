package context

import "github.com/acamarata/cascade/pkg/provider"

// Purpose: BudgetAllocator turns a provider.BudgetConfig into the four slot
//   sizes Assemble fills (R-14.97): tier, retrieval, memory, buffer. Any
//   ratio residual (1.0 minus the four ratios' sum) and any integer-
//   rounding remainder both land on the tier slot, never on the others.
// Inputs: a provider.BudgetConfig.
// Outputs: SlotSizes, or the typed cascade.Error BudgetConfig.Validate
//   already defines for an unusable config (zero/negative MaxTokens, a
//   negative ratio, or ratios summing past 1.0).
// Constraints: pure and deterministic — no clock, no I/O.
//   04-PEWS-PLAN-W1-W3.md Wave 2 Epic E S-09 T1; R-14.97.
// SPORT: context-engine/budget-allocator (ADD, per T-1 sport_updates).

// SlotSizes is the token budget allocated to each of the four slots.
// Tier+Retrieval+Memory+Buffer always equals the source BudgetConfig's
// MaxTokens exactly, by construction: Tier is computed as the remainder
// after the other three, so it absorbs both the configured ratio residual
// and any integer-rounding remainder. Content slots never silently lose
// budget to rounding.
type SlotSizes struct {
	Tier      int
	Retrieval int
	Memory    int
	Buffer    int
}

// BudgetAllocator computes SlotSizes from a validated provider.BudgetConfig.
type BudgetAllocator struct {
	cfg provider.BudgetConfig
}

// NewBudgetAllocator validates cfg (BudgetConfig.Validate) and returns an
// allocator over it, or the typed error Validate produced.
func NewBudgetAllocator(cfg provider.BudgetConfig) (*BudgetAllocator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &BudgetAllocator{cfg: cfg}, nil
}

// Allocate computes the four slot sizes. Retrieval, Memory and Buffer are
// each floor(ratio*MaxTokens); Tier is MaxTokens minus those three, which is
// what makes the residual ratio AND any rounding remainder land on Tier
// (R-14.97): the tier slot, carrying the highest-authority instructions, is
// never the one an under-specified config or integer division shorts.
func (a *BudgetAllocator) Allocate() SlotSizes {
	maxTokens := a.cfg.MaxTokens
	retrieval := int(a.cfg.RetrievalRatio * float64(maxTokens))
	memory := int(a.cfg.MemoryRatio * float64(maxTokens))
	buffer := int(a.cfg.BufferRatio * float64(maxTokens))
	tier := maxTokens - retrieval - memory - buffer
	return SlotSizes{Tier: tier, Retrieval: retrieval, Memory: memory, Buffer: buffer}
}
