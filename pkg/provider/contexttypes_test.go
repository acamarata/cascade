// Purpose: BudgetConfig.Validate's contract tests, and the runnable godoc
//   Example for ContextAssembly (Art.10.6).
// Constraints: external test package (provider_test), like the rest of
//   pkg/provider's tests, so it exercises only the exported surface.
// SPORT: pkg.provider.ContextAssembly,BudgetConfig/ADDED (P1-E05-W2-S09-T1).

package provider_test

import (
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestBudgetConfigValidateAcceptsAnOrdinaryConfig(t *testing.T) {
	cfg := provider.BudgetConfig{MaxTokens: 1000, TierRatio: 0.5, RetrievalRatio: 0.3, MemoryRatio: 0.1, BufferRatio: 0.05}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error %v", err)
	}
}

func TestBudgetConfigValidateAcceptsZeroRatios(t *testing.T) {
	cfg := provider.BudgetConfig{MaxTokens: 1000}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error %v", err)
	}
}

func TestBudgetConfigValidateRefusals(t *testing.T) {
	cases := []struct {
		name string
		cfg  provider.BudgetConfig
	}{
		{"zero max_tokens", provider.BudgetConfig{MaxTokens: 0}},
		{"negative max_tokens", provider.BudgetConfig{MaxTokens: -1}},
		{"negative tier_ratio", provider.BudgetConfig{MaxTokens: 100, TierRatio: -0.1}},
		{"negative retrieval_ratio", provider.BudgetConfig{MaxTokens: 100, RetrievalRatio: -0.1}},
		{"negative memory_ratio", provider.BudgetConfig{MaxTokens: 100, MemoryRatio: -0.1}},
		{"negative buffer_ratio", provider.BudgetConfig{MaxTokens: 100, BufferRatio: -0.1}},
		{"ratios sum over 1.0", provider.BudgetConfig{MaxTokens: 100, TierRatio: 0.5, RetrievalRatio: 0.4, MemoryRatio: 0.1, BufferRatio: 0.1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatalf("Validate(%+v): want error, got nil", tc.cfg)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Errorf("Validate(%+v) kind = %v, ok=%v, want KindInvalidInput", tc.cfg, kind, ok)
			}
		})
	}
}

func TestBudgetConfigValidateAcceptsRatiosSummingToExactlyOne(t *testing.T) {
	cfg := provider.BudgetConfig{MaxTokens: 100, TierRatio: 0.25, RetrievalRatio: 0.25, MemoryRatio: 0.25, BufferRatio: 0.25}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error %v", err)
	}
}

// ExampleContextAssembly demonstrates reading a resolved assembly's slots,
// token accounting, and drop report.
func ExampleContextAssembly() {
	asm := provider.ContextAssembly{
		Tier: []provider.TierBlock{
			{Heading: "Rules", Content: "## Rules\nBe concise.", Role: "GCI", Tokens: 6},
		},
		Retrieval: []provider.RetrievedChunk{
			{ChunkID: "c1", Path: "docs/a.md", CorpusID: "docs", Trust: "trusted", Score: 0.9, Tokens: 12},
		},
		Counts: provider.SlotCounts{Tier: 6, Retrieval: 12, Total: 18},
		Dropped: []provider.DroppedItem{
			{Slot: provider.SlotRetrieval, Detail: "c2", Reason: "retrieval budget exceeded"},
		},
	}

	fmt.Println("tier blocks:", len(asm.Tier))
	fmt.Println("retrieval chunks:", len(asm.Retrieval))
	fmt.Println("total tokens:", asm.Counts.Total)
	fmt.Println("dropped:", len(asm.Dropped), asm.Dropped[0].Reason)

	// Output:
	// tier blocks: 1
	// retrieval chunks: 1
	// total tokens: 18
	// dropped: 1 retrieval budget exceeded
}
