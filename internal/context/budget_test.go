package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: BudgetAllocator's contract tests: refusal propagation from
//   BudgetConfig.Validate, exact-boundary slot-size arithmetic, and that
//   the ratio residual (and any rounding remainder) lands on the tier slot.
//   Also holds the small test doubles assembly_test.go and
//   assemble_scope_test.go share, so neither file grows past the 300-line
//   cap on doubles alone.
// SPORT: context-engine/budget-allocator (ADD, per T-1 sport_updates).

// erroringCounter always fails, for testing that a TokenCounter failure
// propagates as a typed error rather than being swallowed.
type erroringCounter struct{ err error }

func (e erroringCounter) Count(context.Context, string) (int, error) { return 0, e.err }

// fakeResolver is a minimal citations.SourceResolver double for tests that
// need controlled authorization without a real corpus.Store.
type fakeResolver map[string]corpus.Record

func (f fakeResolver) Resolve(chunkID string) (corpus.Record, bool) {
	r, ok := f[chunkID]
	return r, ok
}

// fakeMemoryReader is a minimal provider.MemoryReader double.
type fakeMemoryReader struct {
	items []provider.MemoryItem
	err   error
}

func (f fakeMemoryReader) Fetch(context.Context, int) ([]provider.MemoryItem, error) {
	return f.items, f.err
}

func TestNewBudgetAllocatorRefusesInvalidRatioSum(t *testing.T) {
	cfg := provider.BudgetConfig{MaxTokens: 100, TierRatio: 0.5, RetrievalRatio: 0.6}
	_, err := NewBudgetAllocator(cfg)
	if err == nil {
		t.Fatal("NewBudgetAllocator: want error for ratios summing over 1.0, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("NewBudgetAllocator kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestNewBudgetAllocatorRefusesZeroMaxTokens(t *testing.T) {
	_, err := NewBudgetAllocator(provider.BudgetConfig{MaxTokens: 0})
	if err == nil {
		t.Fatal("NewBudgetAllocator: want error for zero max_tokens, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("NewBudgetAllocator kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestBudgetAllocatorResidualGoesToTierSlot(t *testing.T) {
	// Only 0.3 of the ratio budget is spoken for; the remaining 0.7 must
	// land entirely on Tier.
	cfg := provider.BudgetConfig{MaxTokens: 1000, RetrievalRatio: 0.2, MemoryRatio: 0.1}
	alloc, err := NewBudgetAllocator(cfg)
	if err != nil {
		t.Fatalf("NewBudgetAllocator: %v", err)
	}
	sizes := alloc.Allocate()
	if sizes.Retrieval != 200 || sizes.Memory != 100 || sizes.Buffer != 0 {
		t.Fatalf("Allocate = %+v, want Retrieval=200 Memory=100 Buffer=0", sizes)
	}
	if sizes.Tier != 700 {
		t.Errorf("Allocate.Tier = %d, want 700 (the full ratio residual)", sizes.Tier)
	}
}

func TestBudgetAllocatorRoundingRemainderGoesToTierSlot(t *testing.T) {
	// 333 is not evenly divisible by 3: floor(0.333*333)=110, so if any
	// remainder from that floor were lost rather than credited to Tier,
	// the four slots would not sum back to MaxTokens.
	cfg := provider.BudgetConfig{MaxTokens: 333, RetrievalRatio: 0.333, MemoryRatio: 0.333, BufferRatio: 0.333}
	alloc, err := NewBudgetAllocator(cfg)
	if err != nil {
		t.Fatalf("NewBudgetAllocator: %v", err)
	}
	sizes := alloc.Allocate()
	sum := sizes.Tier + sizes.Retrieval + sizes.Memory + sizes.Buffer
	if sum != cfg.MaxTokens {
		t.Fatalf("slot sizes %+v sum to %d, want exactly MaxTokens %d", sizes, sum, cfg.MaxTokens)
	}
	if sizes.Tier <= 0 {
		t.Errorf("Allocate.Tier = %d, want > 0 to have absorbed the rounding remainder", sizes.Tier)
	}
}

func TestBudgetAllocatorExactBoundaryNoResidual(t *testing.T) {
	cfg := provider.BudgetConfig{MaxTokens: 1000, TierRatio: 0.5, RetrievalRatio: 0.3, MemoryRatio: 0.15, BufferRatio: 0.05}
	alloc, err := NewBudgetAllocator(cfg)
	if err != nil {
		t.Fatalf("NewBudgetAllocator: %v", err)
	}
	sizes := alloc.Allocate()
	want := SlotSizes{Tier: 500, Retrieval: 300, Memory: 150, Buffer: 50}
	if sizes != want {
		t.Errorf("Allocate = %+v, want %+v", sizes, want)
	}
}

// The memory-slot Assemble tests live here, alongside the shared doubles,
// to keep assembly_test.go under the 300-line file cap.

func TestAssembleNilMemoryReaderIsValidNoError(t *testing.T) {
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Reader: nil, Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 100, MemoryRatio: 0.5},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Memory) != 0 || asm.Counts.Memory != 0 {
		t.Errorf("Memory = %+v Counts=%+v, want empty/zero", asm.Memory, asm.Counts)
	}
}

func TestAssembleMemoryReaderFetchErrorIsTypedError(t *testing.T) {
	want := errors.New("memory store unreachable")
	_, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Reader: fakeMemoryReader{err: want},
		Counter: byteLenCounter{}, Budget: provider.BudgetConfig{MaxTokens: 100, MemoryRatio: 0.5},
	})
	if err == nil {
		t.Fatal("Assemble: want error when MemoryReader.Fetch fails, got nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("Assemble error = %v, want it to wrap %v", err, want)
	}
	if _, ok := cascade.KindOf(err); !ok {
		t.Errorf("Assemble error %v carries no taxonomy Kind", err)
	}
}

func TestAssembleMemoryExactBudgetBoundaryNoDrops(t *testing.T) {
	reader := fakeMemoryReader{items: []provider.MemoryItem{{Content: "aaaaa"}, {Content: "bbbbb"}}}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Reader: reader, Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 10, MemoryRatio: 1},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Memory) != 2 || asm.Counts.Memory != 10 {
		t.Fatalf("Memory = %+v Counts=%+v, want two items totalling 10", asm.Memory, asm.Counts)
	}
	if len(asm.Dropped) != 0 {
		t.Errorf("Dropped = %+v, want none at the exact boundary", asm.Dropped)
	}
}

func TestAssembleMemoryOneItemOverBudgetTailTruncates(t *testing.T) {
	reader := fakeMemoryReader{items: []provider.MemoryItem{{Content: "aaaaa"}, {Content: "bbbbbb"}}}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Reader: reader, Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 10, MemoryRatio: 1}, // 5 fits; 5+6=11 is one over
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v", err)
	}
	if len(asm.Memory) != 1 || asm.Counts.Memory != 5 {
		t.Fatalf("Memory = %+v Counts=%+v, want only the first item (5 tokens)", asm.Memory, asm.Counts)
	}
	if len(asm.Dropped) != 1 || asm.Dropped[0].Slot != provider.SlotMemory {
		t.Errorf("Dropped = %+v, want one memory drop", asm.Dropped)
	}
}

func TestAssembleMemorySingleItemLargerThanWholeBudgetIsReported(t *testing.T) {
	reader := fakeMemoryReader{items: []provider.MemoryItem{{Content: strings.Repeat("x", 999)}}}
	asm, err := Assemble(context.Background(), AssembleInput{
		Scope: validScope(), Merged: mergedWith(), Reader: reader, Counter: byteLenCounter{},
		Budget: provider.BudgetConfig{MaxTokens: 10, MemoryRatio: 1},
	})
	if err != nil {
		t.Fatalf("Assemble: unexpected error %v (an oversized memory item must be reported, not erred)", err)
	}
	if len(asm.Memory) != 0 {
		t.Fatalf("Memory = %+v, want the oversized item excluded from content", asm.Memory)
	}
	if len(asm.Dropped) != 1 || asm.Dropped[0].Slot != provider.SlotMemory {
		t.Fatalf("Dropped = %+v, want the oversized memory item explicitly reported", asm.Dropped)
	}
}
