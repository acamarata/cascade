// Package workflows provides harvest gating and Python virtual-environment utilities.
package workflows

import (
	"context"
	"errors"
	"strings"
	"time"
)

// GateRunner defines the interface for vetting and testing harvest operations.
type GateRunner interface {
	Vet(ctx context.Context) error
	Test(ctx context.Context) error
}

// HarvestInput contains the parameters evaluated by the harvest gate.
type HarvestInput struct {
	Greens        int
	SinceLast     time.Duration
	Regenerations int
	Sensitive     bool
	Approvals     []string
}

// ErrHarvestBlocked is returned when input validation fails.
var ErrHarvestBlocked = errors.New("harvest blocked")

// HarvestGate validates the input and, if valid, delegates to the runner.
// It returns false and ErrHarvestBlocked for invalid inputs before invoking any callbacks.
func HarvestGate(ctx context.Context, in HarvestInput, runner GateRunner) (bool, error) {
	if ctx == nil || runner == nil {
		return false, ErrHarvestBlocked
	}
	if in.Greens < 0 || in.Regenerations < 0 {
		return false, ErrHarvestBlocked
	}
	if in.SinceLast < 0 {
		return false, ErrHarvestBlocked
	}

	seen := make(map[string]struct{}, len(in.Approvals))
	for _, a := range in.Approvals {
		if strings.TrimSpace(a) == "" {
			return false, ErrHarvestBlocked
		}
		if _, exists := seen[a]; exists {
			return false, ErrHarvestBlocked
		}
		seen[a] = struct{}{}
	}

	// Valid input: proceed with runner callbacks.
	if err := runner.Vet(ctx); err != nil {
		return false, err
	}
	if err := runner.Test(ctx); err != nil {
		return false, err
	}

	return true, nil
}
