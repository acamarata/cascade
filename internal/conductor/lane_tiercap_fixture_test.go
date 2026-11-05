package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// conductorTestCap is the test-only admit-all cap: every lane may carry
// local-only content.
func conductorTestCap(context.Context, LaneID, LaneType) (provider.SensitivityTier, string, error) {
	return provider.SensitivityLocalOnly, "test-fixture", nil
}
