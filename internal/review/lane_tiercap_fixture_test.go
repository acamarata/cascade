package review

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the review package's test-only lane tier cap (P1-TOP-02j). Every
//   router a review test builds is handed this cap via WithLaneTierCap, so
//   the per-lane ceiling seam is always present in review routing tests.
// Constraints: admit-all at local-only, the most restrictive rank, so it
//   never narrows what the existing sensitivity filter already decides.
// SPORT: internal/review.lane-tiercap-fixture (ADD, P1-TOP-02j).

// reviewTestCap is the test-only admit-all cap: every lane may carry
// local-only content.
func reviewTestCap(context.Context, conductor.LaneID, conductor.LaneType) (provider.SensitivityTier, string, error) {
	return provider.SensitivityLocalOnly, "test-fixture", nil
}
