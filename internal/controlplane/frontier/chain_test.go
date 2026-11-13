// Package frontier provides chain memoisation and longest-path computation.
package frontier

import (
	"errors"
	"testing"
)

// TestChainSurvivesACycle verifies that LongestPath returns ErrCycleDetected on cyclic input.
func TestChainSurvivesACycle(t *testing.T) {
	edges := map[string][]string{
		"A": {"B"},
		"B": {"C"},
		"C": {"A"},
	}
	memo := NewChainMemo()
	_, err := LongestPath("A", edges, memo)
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got %v", err)
	}
}

// TestLongestPathAcyclic verifies correct depth on an acyclic graph.
func TestLongestPathAcyclic(t *testing.T) {
	edges := map[string][]string{
		"A": {"B", "C"},
		"B": {"D"},
		"C": {"D"},
		"D": {},
	}
	memo := NewChainMemo()
	depth, err := LongestPath("A", edges, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if depth != 3 {
		t.Fatalf("expected depth 3, got %d", depth)
	}
}

// TestChainMemoCaches verifies that ChainMemo caches results across calls.
func TestChainMemoCaches(t *testing.T) {
	edges := map[string][]string{
		"A": {"B"},
		"B": {},
	}
	memo := NewChainMemo()
	d1, err := LongestPath("A", edges, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d2, err := LongestPath("A", edges, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("memo mismatch: %d vs %d", d1, d2)
	}
	if _, ok := memo.Get("A"); !ok {
		t.Fatal("expected A to be cached in memo")
	}
}
