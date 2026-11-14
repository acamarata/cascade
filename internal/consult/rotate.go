// Package consult provides helpers for deciding when a consult session
// should rotate to a fresh context.
package consult

import "errors"

// ErrInvalidRotationInput is returned when rotation inputs violate the
// expected invariants: baseline must be non-negative, threshold must be
// non-negative, and context must not be smaller than baseline.
var ErrInvalidRotationInput = errors.New("consult: invalid rotation input")

// ShouldRotate reports whether rotation should occur after the context has
// grown strictly beyond growthThreshold tokens relative to baselineTokens.
//
// It returns false and ErrInvalidRotationInput for invalid input, failing
// closed instead of rotating. The function is pure and performs no I/O.
func ShouldRotate(contextTokens, baselineTokens, growthThreshold int) (bool, error) {
	if baselineTokens < 0 || growthThreshold < 0 || contextTokens < baselineTokens {
		return false, ErrInvalidRotationInput
	}

	growth := contextTokens - baselineTokens
	return growth > growthThreshold, nil
}
