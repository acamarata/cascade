package context

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSummarizer is a scripted SummarizerGetter double. It lives in
// _test.go only, per Art.1: the real seam is T2's rolling summarizer.
type fakeSummarizer struct {
	sub Slot
	err error
}

func (f fakeSummarizer) Summarize(context.Context, Slot, int) (Slot, error) {
	return f.sub, f.err
}

func mustComposer(t *testing.T, counter interface {
	Count(context.Context, string) (int, error)
}, sg SummarizerGetter) *Composer {
	t.Helper()
	c, err := NewComposer(tokenCounterAdapter{counter}, sg)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	return c
}

// tokenCounterAdapter lets the test helper accept fixedTokenCounter /
// erroringCounter (already declared in this package by assembly_test.go
// and budget_test.go) without re-importing provider.TokenCounter by name
// in every call site.
type tokenCounterAdapter struct {
	inner interface {
		Count(context.Context, string) (int, error)
	}
}

func (a tokenCounterAdapter) Count(ctx context.Context, text string) (int, error) {
	return a.inner.Count(ctx, text)
}

func TestComposeNilContext(t *testing.T) {
	c := mustComposer(t, fixedTokenCounter{n: 1}, nil)
	_, err := c.Compose(nil, 10, []Slot{{Content: "x"}}) //nolint:staticcheck // deliberate nil-ctx test
	requireKind(t, err, cascade.KindInvalidInput)
}

func TestComposeNonPositiveBudget(t *testing.T) {
	c := mustComposer(t, fixedTokenCounter{n: 1}, nil)
	for _, budget := range []int{0, -1, -100} {
		_, err := c.Compose(context.Background(), budget, []Slot{{Content: "x"}})
		requireKind(t, err, cascade.KindInvalidInput)
	}
}

func TestComposeAllSlotsEmpty(t *testing.T) {
	c := mustComposer(t, fixedTokenCounter{n: 1}, nil)
	cases := [][]Slot{
		nil,
		{},
		{{Content: ""}, {Content: ""}},
	}
	for _, slots := range cases {
		res, err := c.Compose(context.Background(), 100, slots)
		if err != nil {
			t.Fatalf("Compose(all-empty): unexpected error %v", err)
		}
		if res.ZeroSlots == nil {
			t.Error("Compose(all-empty): want ZeroSlots event")
		}
		if res.TokensUsed != 0 || len(res.Slots) != 0 {
			t.Errorf("Compose(all-empty): want empty result, got %+v", res)
		}
	}
}

func TestComposeFitsWhole(t *testing.T) {
	c := mustComposer(t, fixedTokenCounter{n: 10}, nil)
	slots := []Slot{
		{Kind: SlotKindTier, Label: "gci", Content: "a"},
		{Kind: SlotKindMemory, Label: "soul", Content: "b"},
		{Kind: SlotKindRetrieval, Label: "chunk", Content: "c"},
	}
	res, err := c.Compose(context.Background(), 100, slots)
	if err != nil {
		t.Fatalf("Compose: unexpected error %v", err)
	}
	if res.TokensUsed != 30 || len(res.Slots) != 3 || len(res.Trims) != 0 {
		t.Fatalf("Compose: got %+v", res)
	}
}

func TestComposeSingleOversizedSlotErrors(t *testing.T) {
	c := mustComposer(t, fixedTokenCounter{n: 999}, nil)
	_, err := c.Compose(context.Background(), 10, []Slot{{Kind: SlotKindTier, Label: "gci", Content: "x"}})
	requireKind(t, err, cascade.KindInvalidInput)
}

func TestComposeLaterOversizedSlotDropsWithEvent(t *testing.T) {
	// First slot fits; a large fixed counter would also make the first
	// slot oversized, so this test drives per-slot sizes through the
	// summarizer-less fallback using a scripted sequence via two calls
	// with distinct fixed counters is not expressible with the shared
	// fixedTokenCounter (same n for every Count call), so this exercises
	// the drop path directly: a small budget where the ONLY slot exceeds
	// remaining after slot 0 already consumed some of it.
	c := mustComposer(t, &sequenceCounter{ns: []int{5, 20}}, nil)
	slots := []Slot{
		{Kind: SlotKindTier, Label: "gci", Content: "a"},
		{Kind: SlotKindRetrieval, Label: "chunk", Content: "b"},
	}
	res, err := c.Compose(context.Background(), 10, slots)
	if err != nil {
		t.Fatalf("Compose: unexpected error %v", err)
	}
	if len(res.Slots) != 1 || res.Slots[0].Label != "gci" {
		t.Fatalf("Compose: want only gci to survive, got %+v", res.Slots)
	}
	if len(res.Trims) != 1 || !res.Trims[0].Dropped || res.Trims[0].Label != "chunk" {
		t.Fatalf("Compose: want a drop event for chunk, got %+v", res.Trims)
	}
	if res.TokensUsed != 5 || res.TokensUsed > res.Budget {
		t.Fatalf("Compose: TokensUsed=%d budget=%d", res.TokensUsed, res.Budget)
	}
}

func TestComposeSummarizerSubstituteFits(t *testing.T) {
	sub := Slot{Kind: SlotKindHistory, Label: "hist", Content: "short"}
	sg := fakeSummarizer{sub: sub}
	c := mustComposer(t, &sequenceCounter{ns: []int{5, 50, 3}}, sg)
	slots := []Slot{
		{Kind: SlotKindTier, Label: "gci", Content: "a"},
		{Kind: SlotKindHistory, Label: "hist", Content: "a very long turn"},
	}
	res, err := c.Compose(context.Background(), 10, slots)
	if err != nil {
		t.Fatalf("Compose: unexpected error %v", err)
	}
	if len(res.Slots) != 2 || !res.Slots[1].Summarized {
		t.Fatalf("Compose: want summarized substitute included, got %+v", res.Slots)
	}
	if len(res.Trims) != 0 {
		t.Fatalf("Compose: want no trim events when summarizer succeeds, got %+v", res.Trims)
	}
	if res.TokensUsed > res.Budget {
		t.Fatalf("Compose: TokensUsed=%d exceeds Budget=%d", res.TokensUsed, res.Budget)
	}
}

func TestComposeSummarizerErrorFallsBackToDrop(t *testing.T) {
	sg := fakeSummarizer{err: errors.New("summarizer unavailable")}
	c := mustComposer(t, &sequenceCounter{ns: []int{5, 50}}, sg)
	slots := []Slot{
		{Kind: SlotKindTier, Label: "gci", Content: "a"},
		{Kind: SlotKindHistory, Label: "hist", Content: "long"},
	}
	res, err := c.Compose(context.Background(), 10, slots)
	if err != nil {
		t.Fatalf("Compose: unexpected error %v", err)
	}
	if len(res.Trims) != 1 || !res.Trims[0].Dropped || res.Trims[0].Reason == "" {
		t.Fatalf("Compose: want a drop event with a reason, got %+v", res.Trims)
	}
	for _, ev := range res.Trims {
		if ev.Reason == "long" {
			t.Fatal("Compose: BudgetTrimEvent.Reason must never echo slot content")
		}
	}
}

func TestComposeSummarizerSubstituteStillOversizedDrops(t *testing.T) {
	sub := Slot{Kind: SlotKindHistory, Label: "hist", Content: "still too big"}
	sg := fakeSummarizer{sub: sub}
	c := mustComposer(t, &sequenceCounter{ns: []int{5, 50, 40}}, sg)
	slots := []Slot{
		{Kind: SlotKindTier, Label: "gci", Content: "a"},
		{Kind: SlotKindHistory, Label: "hist", Content: "long"},
	}
	res, err := c.Compose(context.Background(), 10, slots)
	if err != nil {
		t.Fatalf("Compose: unexpected error %v", err)
	}
	if len(res.Trims) != 1 || !res.Trims[0].Dropped {
		t.Fatalf("Compose: want a drop event, got %+v", res.Trims)
	}
}

func TestComposeDependencyCounterErrorPreservesKind(t *testing.T) {
	c := mustComposer(t, erroringCounter{err: cascade.New(cascade.KindUnavailable, "counter down")}, nil)
	_, err := c.Compose(context.Background(), 10, []Slot{{Content: "x"}})
	requireKind(t, err, cascade.KindUnavailable)
}

func TestComposeDeterministic(t *testing.T) {
	c := mustComposer(t, fixedTokenCounter{n: 3}, nil)
	slots := []Slot{{Kind: SlotKindTier, Label: "gci", Content: "a"}, {Kind: SlotKindMemory, Label: "m", Content: "b"}}
	a, err := c.Compose(context.Background(), 10, slots)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	b, err := c.Compose(context.Background(), 10, slots)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if a.TokensUsed != b.TokensUsed || len(a.Slots) != len(b.Slots) {
		t.Fatalf("Compose: non-deterministic results %+v vs %+v", a, b)
	}
}

// sequenceCounter returns successive values from ns on each Count call,
// for tests that need distinct sizes per slot -- fixedTokenCounter always
// returns the same n, which cannot express "slot A is small, slot B is
// large" in one Composer.
type sequenceCounter struct {
	ns []int
	i  int
}

func (s *sequenceCounter) Count(context.Context, string) (int, error) {
	n := s.ns[s.i]
	if s.i < len(s.ns)-1 {
		s.i++
	}
	return n, nil
}

func requireKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if err == nil {
		t.Fatal("want a typed error, got nil")
	}
	k, ok := cascade.KindOf(err)
	if !ok {
		t.Fatalf("error %v carries no taxonomy kind", err)
	}
	if k != want {
		t.Fatalf("error kind = %v, want %v", k, want)
	}
}
