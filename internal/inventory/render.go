// Purpose: render counts.json's exact bytes from a fresh ComputeTreeCounts
//
//	call, shared by internal/inventory/gen/main.go (writes the file) and
//	this package's own idempotency test (calls it twice and diffs).
//
// Inputs: repoRoot, and a runtime.Clock for the generated_at stamp (Art.7.3:
//
//	no bare time.Now — the caller injects runtime.NewSystemClock() in
//	production, runtime.NewFixedClock in tests).
//
// Outputs: the exact bytes counts.json should hold.
// Constraints: deterministic for a fixed clock and an unchanged tree —
//
//	encoding/json.MarshalIndent over a struct with fixed field order plus
//	tree.go's sorted Platforms slice guarantees this without a custom
//	encoder.
//
// SPORT: internal.inventory.RenderCounts/ADDED.

package inventory

import (
	"encoding/json"
	"fmt"

	"github.com/acamarata/cascade/internal/runtime"
)

// RenderCounts computes TreeCounts against repoRoot and marshals them, plus
// clock's current instant, into counts.json's exact byte shape (trailing
// newline included, matching gofmt-adjacent JSON file convention in this
// tree).
func RenderCounts(repoRoot string, clock runtime.Clock) ([]byte, error) {
	tc, err := ComputeTreeCounts(repoRoot)
	if err != nil {
		return nil, err
	}
	g := GeneratedCounts{
		GeneratedAt: clock.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Providers:   tc.Providers,
		Plugins:     tc.Plugins,
		SPORTLines:  tc.SPORTLines,
		Platforms:   tc.Platforms,
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("inventory: marshal counts.json: %w", err)
	}
	return append(data, '\n'), nil
}
