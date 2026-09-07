//go:build windows

package governor

// Purpose: windows's collectMetrics implementation — tier-2 per the
//
//	contract: no real metric collection, a documented sentinel refusal.
//
// Inputs: none.
// Outputs: a zero ResourceSnapshot and ErrUnsupportedPlatform, always.
// Constraints: Art.5 platform parity requires the windows build to
//
//	compile and Sampler.Start/Snapshot to run without panicking even
//	though this platform has no real collector yet; it must never invent
//	metrics to look supported.
//
// SPORT: internal/fleet/governor.Sampler (ADD, per T-1 sport_updates).

// collectMetrics implements this package's platform seam for windows. It
// is tier-2: no metric source is wired yet, so it always refuses via the
// package's ErrUnsupportedPlatform sentinel rather than returning
// fabricated data.
func collectMetrics() (ResourceSnapshot, error) {
	return ResourceSnapshot{}, ErrUnsupportedPlatform
}
