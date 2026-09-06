package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: ContextAssembler contract tests: budget-boundary enforcement per
//   slot (exact limit, one token over, a single item larger than the whole
//   budget), tier-order precedence under trim, empty-input graceful paths,
//   and every documented error path.
// SPORT: context-engine/assembly (ADD, per T-1 sport_updates).

// byteLenCounter counts a text's byte length, so a test can craft an exact
// token budget by choosing a string's exact length.
type byteLenCounter struct{}

func (byteLenCounter) Count(_ context.Context, text string) (int, error) {
	return len(text), nil
}

// fixedTokenCounter reports n for any non-empty text, decoupling a
// retrieval/memory boundary test from citations.Render's exact output.
type fixedTokenCounter struct{ n int }

func (f fixedTokenCounter) Count(_ context.Context, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	return f.n, nil
}

func validScope() scope.SessionScope {
	return scope.SessionScope{Kind: scope.ScopeKindSession, Session: "s1", Project: "proj1"}
}

func mergedWith(sections ...MergedSection) *MergedContext {
	return &MergedContext{Sections: sections, Provenance: map[string]TierRole{}}
}

// TestAssembleInputValidationIsTypedError covers the four input-shape
// refusals Assemble must never let through as a panic or a silent zero
// value: a nil MergedContext, a nil TokenCounter, an invalid SessionScope
// Kind, and a zero budget.
func TestAssembleInputValidationIsTypedError(t *testing.T) {
	cases := []struct {
		name  string
		input AssembleInput
	}{
		{"nil MergedContext", AssembleInput{Scope: validScope(), Merged: nil, Counter: byteLenCounter{}, Budget: provider.BudgetConfig{MaxTokens: 100}}},
		{"nil TokenCounter", AssembleInput{Scope: validScope(), Merged: mergedWith(), Counter: nil, Budget: provider.BudgetConfig{MaxTokens: 100}}},
		{"invalid scope kind", AssembleInput{Scope: scope.SessionScope{}, Merged: mergedWith(), Counter: byteLenCounter{}, Budget: provider.BudgetConfig{MaxTokens: 100}}},
		{"zero budget", AssembleInput{Scope: validScope(), Merged: mergedWith(), Counter: byteLenCounter{}, Budget: provider.BudgetConfig{MaxTokens: 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Assemble(context.Background(), tc.input)
			requireInvalidInput(t, err, tc.name)
		})
	}
}

func TestAssembleBudgetTooSmallForHighestTierBlockIsTypedError(t *testing.T) {
	tierSec := MergedSection{Heading: "Rules", Content: strings.Repeat("x", 10), Role: TierGCI, Ordinal: 0}
	_, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(tierSec), Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 9}, // tier gets all 9; block needs 10
	})
	requireInvalidInput(t, err, "budget too small for the highest-tier block")
}

func TestAssembleExactBudgetBoundaryNoError(t *testing.T) {
	tierSec := MergedSection{Heading: "Rules", Content: strings.Repeat("x", 10), Role: TierGCI, Ordinal: 0}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(tierSec), Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 10}, // exactly fits, no overflow
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Tier) != 1 || asm.Counts.Tier != 10 {
		t.Fatalf("Assemble = %+v, want one tier block totalling 10 tokens", asm)
	}
	if len(asm.Dropped) != 0 {
		t.Errorf("Dropped = %+v, want none at the exact boundary", asm.Dropped)
	}
}

func TestAssembleOneTokenOverBoundaryTailTruncatesSecondSection(t *testing.T) {
	first := MergedSection{Heading: "A", Content: strings.Repeat("a", 5), Role: TierGCI, Ordinal: 0}
	second := MergedSection{Heading: "B", Content: strings.Repeat("b", 5), Role: TierPAI, Ordinal: 4}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(first, second), Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 9}, // first (5) fits; first+second (10) is one over 9
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Tier) != 1 || asm.Tier[0].Heading != "A" {
		t.Fatalf("Tier = %+v, want only section A to survive", asm.Tier)
	}
	if len(asm.Dropped) != 1 || asm.Dropped[0].Slot != provider.SlotTier || asm.Dropped[0].Reason == "" {
		t.Errorf("Dropped = %+v, want one tier drop naming a reason", asm.Dropped)
	}
}

func TestAssembleTierOrderPrecedenceGCISurvivesOverPAI(t *testing.T) {
	gci := MergedSection{Heading: "Rules", Content: strings.Repeat("g", 8), Role: TierGCI, Ordinal: 0}
	pai := MergedSection{Heading: "App", Content: strings.Repeat("p", 8), Role: TierPAI, Ordinal: 4}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(gci, pai), Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 8}, // only room for one section
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Tier) != 1 || asm.Tier[0].Role != "GCI" {
		t.Fatalf("Tier = %+v, want GCI alone to survive the trim over PAI", asm.Tier)
	}
	if len(asm.Dropped) != 1 || asm.Dropped[0].Detail == "" {
		t.Errorf("Dropped = %+v, want the PAI section named", asm.Dropped)
	}
}

func TestAssembleEmptyRetrievalListIsValidNoError(t *testing.T) {
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Ranked: nil, Resolver: nil,
		Counter: byteLenCounter{}, Budget: provider.BudgetConfig{MaxTokens: 100, RetrievalRatio: 0.5},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Retrieval) != 0 || asm.Counts.Retrieval != 0 {
		t.Errorf("Retrieval = %+v Counts=%+v, want empty/zero", asm.Retrieval, asm.Counts)
	}
}

func TestAssembleTokenCounterErrorIsTypedError(t *testing.T) {
	want := errors.New("tokenizer down")
	tierSec := MergedSection{Heading: "Rules", Content: "some text", Role: TierGCI, Ordinal: 0}
	_, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(tierSec), Counter: erroringCounter{err: want},
		Budget: provider.BudgetConfig{MaxTokens: 100},
	})
	if !errors.Is(err, want) {
		t.Errorf("Assemble error = %v, want it to wrap %v", err, want)
	}
}

func fusedResult(id, path, corpusID string) rrf.FusedResult {
	return rrf.FusedResult{
		ChunkID: id, Path: path, CorpusID: corpusID, Trust: corpus.TrustTrusted,
		Score: 0.5, RawScore: 1, Strategies: []rrf.StrategyName{rrf.StrategyFTS},
	}
}

func TestAssembleRetrievalExactBudgetBoundaryNoDrops(t *testing.T) {
	resolver := fakeResolver{
		"c1": corpus.Record{ID: "c1", CorpusID: "docs", ScopeRef: "s1", Trust: corpus.TrustTrusted},
		"c2": corpus.Record{ID: "c2", CorpusID: "docs", ScopeRef: "s1", Trust: corpus.TrustTrusted},
	}
	ranked := []rrf.FusedResult{fusedResult("c1", "a.md", "docs"), fusedResult("c2", "b.md", "docs")}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Ranked: ranked, Resolver: resolver,
		Counter: fixedTokenCounter{n: 10}, Budget: provider.BudgetConfig{MaxTokens: 20, RetrievalRatio: 1},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Retrieval) != 2 || asm.Counts.Retrieval != 20 {
		t.Fatalf("Retrieval = %+v Counts=%+v, want two chunks totalling 20", asm.Retrieval, asm.Counts)
	}
	if len(asm.Dropped) != 0 {
		t.Errorf("Dropped = %+v, want none at the exact boundary", asm.Dropped)
	}
}

func TestAssembleRetrievalSingleItemLargerThanWholeBudgetIsReportedNotSkipped(t *testing.T) {
	resolver := fakeResolver{"c1": corpus.Record{ID: "c1", CorpusID: "docs", ScopeRef: "s1", Trust: corpus.TrustTrusted}}
	ranked := []rrf.FusedResult{fusedResult("c1", "a.md", "docs")}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Ranked: ranked, Resolver: resolver,
		Counter: fixedTokenCounter{n: 999}, Budget: provider.BudgetConfig{MaxTokens: 10, RetrievalRatio: 1},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v (an oversized single retrieval item must be reported, not erred)", err)
	}
	if len(asm.Retrieval) != 0 {
		t.Fatalf("Retrieval = %+v, want the oversized chunk excluded from content", asm.Retrieval)
	}
	if len(asm.Dropped) != 1 || asm.Dropped[0].Detail != "c1" {
		t.Fatalf("Dropped = %+v, want the oversized chunk c1 explicitly reported", asm.Dropped)
	}
}

func TestAssembleRetrievalRequiresResolverWhenRankedIsNonEmpty(t *testing.T) {
	ranked := []rrf.FusedResult{fusedResult("c1", "a.md", "docs")}
	_, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Ranked: ranked, Resolver: nil,
		Counter: fixedTokenCounter{n: 1}, Budget: provider.BudgetConfig{MaxTokens: 10, RetrievalRatio: 1},
	})
	requireInvalidInput(t, err, "nil resolver with non-empty ranked results")
}

func TestAssembleTierPreambleDropDetail(t *testing.T) {
	first := MergedSection{Heading: "Rules", Content: strings.Repeat("a", 5), Role: TierGCI, Ordinal: 0}
	preamble := MergedSection{Heading: "", Content: strings.Repeat("b", 5), Role: TierASI, Ordinal: 1}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(first, preamble), Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 5},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Dropped) != 1 || !strings.Contains(asm.Dropped[0].Detail, "preamble") {
		t.Errorf("Dropped = %+v, want the dropped preamble named as such", asm.Dropped)
	}
}

func requireInvalidInput(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want error, got nil", label)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("%s: kind = %v, ok=%v, want KindInvalidInput", label, kind, ok)
	}
}
