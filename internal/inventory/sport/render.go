// Purpose: render registry.json's exact bytes, shared by
//
//	internal/inventory/gen/main.go (writes the file) and this package's
//	own idempotency test.
//
// Constraints: deterministic for a fixed clock and an unchanged tree,
//
//	matching internal/inventory/render.go's RenderCounts precedent.
//
// SPORT: internal.inventory.sport.RenderRegistry/ADDED.

package sport

import (
	"encoding/json"
	"fmt"

	"github.com/acamarata/cascade/internal/runtime"
)

// RenderRegistry computes ComputeRegistry against repoRoot and marshals
// it, stamped with clock's current instant, into registry.json's exact
// byte shape (trailing newline included).
func RenderRegistry(repoRoot string, clock runtime.Clock) ([]byte, error) {
	reg, err := ComputeRegistry(repoRoot, clock.Now().UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sport: marshal registry.json: %w", err)
	}
	return append(data, '\n'), nil
}
