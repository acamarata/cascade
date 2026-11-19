package graph

import (
	"errors"
	"fmt"
)

// BindingTierFloor is the minimum coverage percentage that must be maintained.
const BindingTierFloor = 85.0

// ErrBelowCoverageFloor is returned when the current coverage falls below the effective floor.
var ErrBelowCoverageFloor = errors.New("coverage is below the required floor")

// CheckCoverageFloor verifies that the current coverage meets or exceeds the
// effective floor, which is the greater of BindingTierFloor and live.
// It returns nil when current is sufficient, or an error otherwise.
func CheckCoverageFloor(current, live float64) error {
	floor := BindingTierFloor
	if live > floor {
		floor = live
	}
	if current < floor {
		return fmt.Errorf("%w: current %.2f < floor %.2f", ErrBelowCoverageFloor, current, floor)
	}
	return nil
}
