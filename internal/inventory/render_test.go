package inventory

import (
	"bytes"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestRenderCounts_Idempotent proves the generator's core computation is
// deterministic: two RenderCounts calls against the same unchanged tree
// with the same fixed clock instant produce byte-identical output. This
// is item 2's "regenerating must be idempotent" requirement. A real
// `go run ./internal/inventory/gen` run picks up the wall clock (via
// runtime.NewSystemClock), so its generated_at timestamp legitimately
// differs between two real runs seconds apart — that is the ONLY field
// that can differ, and injecting a FixedClock here removes it from the
// comparison so the test isolates the tree-derived fields, which must
// never differ for an unchanged tree.
func TestRenderCounts_Idempotent(t *testing.T) {
	root := buildFixtureRepo(t)
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	first, err := RenderCounts(root, clock)
	if err != nil {
		t.Fatalf("RenderCounts (first): %v", err)
	}
	second, err := RenderCounts(root, clock)
	if err != nil {
		t.Fatalf("RenderCounts (second): %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("RenderCounts not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestRenderCounts_MissingTree(t *testing.T) {
	root := t.TempDir()
	clock := runtime.NewFixedClock(time.Now())
	if _, err := RenderCounts(root, clock); err == nil {
		t.Fatal("RenderCounts: want error for a tree with no providers/, got nil")
	}
}
