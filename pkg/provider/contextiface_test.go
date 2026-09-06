// Purpose: contract tests for MemoryReader and TokenCounter: that both are
//   satisfiable by a minimal double, and that a Fetch/Count failure carries
//   an error rather than a zero value dressed up as success.
// Constraints: every double here exists ONLY in this _test.go file
//   (Art.1.1); no implementation ships from this ticket except
//   NaiveTokenCounter (tokencounter.go), which has its own test file.
// SPORT: pkg.provider.MemoryReader,TokenCounter/ADDED (P1-E05-W2-S09-T1).

package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

type fakeMemoryReader struct {
	items []provider.MemoryItem
	err   error
}

func (f fakeMemoryReader) Fetch(_ context.Context, _ int) ([]provider.MemoryItem, error) {
	return f.items, f.err
}

type fakeTokenCounter struct {
	n   int
	err error
}

func (f fakeTokenCounter) Count(_ context.Context, _ string) (int, error) {
	return f.n, f.err
}

func TestMemoryReaderFetchReturnsItems(t *testing.T) {
	var r provider.MemoryReader = fakeMemoryReader{items: []provider.MemoryItem{{Content: "note"}}}
	items, err := r.Fetch(context.Background(), 100)
	if err != nil {
		t.Fatalf("Fetch: unexpected error %v", err)
	}
	if len(items) != 1 || items[0].Content != "note" {
		t.Errorf("Fetch = %+v, want one item with content %q", items, "note")
	}
}

func TestMemoryReaderFetchPropagatesError(t *testing.T) {
	want := errors.New("memory source unreachable")
	var r provider.MemoryReader = fakeMemoryReader{err: want}
	items, err := r.Fetch(context.Background(), 100)
	if !errors.Is(err, want) {
		t.Errorf("Fetch error = %v, want wrapping %v", err, want)
	}
	if items != nil {
		t.Errorf("Fetch items = %+v on error, want nil", items)
	}
}

func TestTokenCounterCount(t *testing.T) {
	var c provider.TokenCounter = fakeTokenCounter{n: 5}
	n, err := c.Count(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Count: unexpected error %v", err)
	}
	if n != 5 {
		t.Errorf("Count = %d, want 5", n)
	}
}

func TestTokenCounterCountPropagatesError(t *testing.T) {
	want := errors.New("tokenizer unavailable")
	var c provider.TokenCounter = fakeTokenCounter{err: want}
	if _, err := c.Count(context.Background(), "x"); !errors.Is(err, want) {
		t.Errorf("Count error = %v, want wrapping %v", err, want)
	}
}
