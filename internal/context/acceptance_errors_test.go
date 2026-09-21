// Package context (acceptance_errors_test.go): Purpose: the P1-E21-W5-
// S46-T5 error-path half of the bounded-context/rolling-summary acceptance
// story (12-QUALITY-CONSTITUTION.md Art.3: happy-path-only is a CR-A
// blocking finding). Each subtest drives a REAL shipped unit with the
// specific failing input the ticket names.
// Inputs: a real Composer given a zero budget; a real Summarizer given
// empty source content.
// Outputs: none (t.Fatal only).
// Constraints: no network; every assertion names the concrete input.
// SPORT: internal/context acceptance (ADD) (P1-E21-W5-S46-T5).
package context

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestAcceptanceErrorPaths is the checks-list entry point.
func TestAcceptanceErrorPaths(t *testing.T) {
	t.Run("ComposerZeroBudget", acceptanceComposerZeroBudget)
	t.Run("RollingSummaryNoConversationHistory", acceptanceRollingSummaryNoHistory)
}

// acceptanceComposerZeroBudget is "Composer given a budget of 0 -> hard
// error returned, process does not panic": the real Compose refuses a
// non-positive budget with a typed KindInvalidInput error before touching
// any slot or dependency.
func acceptanceComposerZeroBudget(t *testing.T) {
	composer, err := NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	result, err := composer.Compose(context.Background(), 0, []Slot{{Kind: SlotKindTier, Label: "gci", Content: "x"}})
	if err == nil {
		t.Fatalf("Compose(budget=0) = %+v, nil error; want a hard, typed error", result)
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Compose(budget=0) error = %v, want KindInvalidInput", err)
	}
	if result.TokensUsed != 0 || len(result.Slots) != 0 {
		t.Fatalf("Compose(budget=0) result = %+v, want the zero ComposedResult alongside the error", result)
	}
}

// acceptanceRollingSummaryNoHistory is "Rolling-summary called with no
// conversation history -> empty result, no panic": GetSummary is driven
// with empty source content (no prior stored record either), and the real
// pipeline - buildModelRequest's zero-length window, dispatchExecute,
// checkOutputSize - runs end to end without panicking, producing the
// fake executor's own empty reply as a legitimate empty SummaryOutcome.
func acceptanceRollingSummaryNoHistory(t *testing.T) {
	exec := &fakeExecutor{output: ""}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	outcome, err := s.GetSummary(context.Background(), "acceptance-empty-history", GranularityTurnWindow, "", "v-empty")
	if err != nil {
		t.Fatalf("GetSummary(no history) = %v, want no error (caller misuse only; empty content is not "+
			"caller misuse)", err)
	}
	if outcome.Record.Content != "" {
		t.Fatalf("GetSummary(no history) = %+v, want an empty Content (nothing was ever available to "+
			"summarize)", outcome)
	}
	if outcome.Failed != nil {
		t.Fatalf("GetSummary(no history) reported Failed = %+v, want no degradation: an empty reply to an "+
			"empty window is not a regeneration failure", outcome.Failed)
	}
}
