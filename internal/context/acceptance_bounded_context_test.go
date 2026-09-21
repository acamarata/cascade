// Package context (acceptance_bounded_context_test.go): Purpose: the
// P1-E21-W5-S46-T5 Story 2 bounded-context acceptance proof: a synthetic
// conversation whose full content vastly exceeds a configured budget is
// driven through the REAL Compose (composer_core.go, S-46.T1) wired to the
// REAL rolling summarizer (summarizer_core.go, S-46.T2) via a fake
// provider.ModelExecutor at the true provider seam - never a double of
// Compose or Summarizer, the units under acceptance.
// Inputs: a hand-built slot list modelling one small always-kept tier slot
// plus a growing run of conversation-history slots, at four budgets
// spanning far below to far above the full content size.
// Outputs: none (t.Fatal only).
// Constraints: no network; provider.NaiveTokenCounter{} (a real, shipped
// counter, R-14.97) measures every slot, never a fake TokenCounter, since
// the counter is not the unit under acceptance either.
// SPORT: internal/context acceptance (ADD) (P1-E21-W5-S46-T5).
package context

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// acceptanceSyntheticConversation builds n history slots of strictly
// growing size, prefixed by one small tier slot that fits at every budget
// this file exercises - the "synthetic conversation exceeding the
// configured budget" the ticket names, sized so the invariant is actually
// exercised (some budgets drop or summarize slots) rather than trivially
// satisfied (every budget fits everything whole).
func acceptanceSyntheticConversation(n int) []Slot {
	slots := make([]Slot, 0, n+1)
	slots = append(slots, Slot{Kind: SlotKindTier, Label: "gci", Content: "core instructions, always kept"})
	for i := 1; i <= n; i++ {
		slots = append(slots, Slot{
			Kind:    SlotKindHistory,
			Label:   "turn",
			Content: strings.Repeat("the conversation continues with more detail each turn ", i),
		})
	}
	return slots
}

// TestBoundedContextInvariant is the checks-list entry point: at every one
// of four widely spaced budgets, the real Compose over a real Summarizer
// substitution seam never returns a result whose TokensUsed exceeds its
// own Budget.
func TestBoundedContextInvariant(t *testing.T) {
	exec := &fakeExecutor{output: "a short rolling summary of the material so far"}
	summarizer := newTestSummarizer(t, exec, storetest.NewMemStore())
	composer, err := NewComposer(provider.NaiveTokenCounter{}, summarizer)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	slots := acceptanceSyntheticConversation(12)
	fullSize := 0
	for _, s := range slots {
		n, _ := provider.NaiveTokenCounter{}.Count(context.Background(), s.Content)
		fullSize += n
	}

	sawATrim := false
	for _, budget := range []int{20, 200, 1000, fullSize * 3} {
		t.Run(budgetSubtestName(budget), func(t *testing.T) {
			result, err := composer.Compose(context.Background(), budget, slots)
			if err != nil {
				t.Fatalf("Compose(budget=%d): %v (a refusal here would still be invariant-compliant, but "+
					"this budget was chosen so the highest-priority slot always fits)", budget, err)
			}
			if result.TokensUsed > result.Budget {
				t.Fatalf("Compose(budget=%d) = TokensUsed %d > Budget %d: bounded-context invariant "+
					"violated", budget, result.TokensUsed, result.Budget)
			}
			if result.Budget != budget {
				t.Fatalf("result.Budget = %d, want the budget Compose was called with (%d)", result.Budget, budget)
			}
			if len(result.Trims) > 0 {
				sawATrim = true
			}
		})
	}
	if !sawATrim {
		t.Fatalf("no budget in this run ever trimmed or dropped a slot: the invariant was trivially "+
			"satisfied rather than exercised (full content measures %d tokens)", fullSize)
	}
}

// budgetSubtestName gives each budget its own === RUN line.
func budgetSubtestName(budget int) string {
	switch {
	case budget < 100:
		return "far_below_full_content"
	case budget < 500:
		return "below_full_content"
	case budget < 2000:
		return "near_full_content"
	default:
		return "above_full_content"
	}
}
