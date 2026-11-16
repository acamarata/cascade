package fmx

import (
	"errors"
)

// ErrBelowAgentFloor is returned when the configured agent count is below 100.
var ErrBelowAgentFloor = errors.New("agent count below floor of 100")

// ErrNotContinuous is returned when samples show active agents below 100 or when no samples are provided.
var ErrNotContinuous = errors.New("lane capacity not continuous")

// LaneConfig holds the configuration for a gateway lane.
type LaneConfig struct {
	// Agents is the number of agents configured for the lane.
	Agents int
}

// LaneLoadSample represents a single load measurement from the lane.
type LaneLoadSample struct {
	// Active is the number of active agents during this sample period.
	Active int
}

// ValidateLaneCapacity validates that the gateway lane can support at least 100 agents
// running continuously. It returns ErrBelowAgentFloor if cfg.Agents is under 100,
// and ErrNotContinuous if any sample shows active agents under 100 or if no samples are provided.
func ValidateLaneCapacity(cfg LaneConfig, samples []LaneLoadSample) error {
	if cfg.Agents < 100 {
		return ErrBelowAgentFloor
	}

	if len(samples) == 0 {
		return ErrNotContinuous
	}

	for _, s := range samples {
		if s.Active < 100 {
			return ErrNotContinuous
		}
	}

	return nil
}
