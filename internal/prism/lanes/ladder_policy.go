// Package lanes provides ladder policy types and configuration reading.
package lanes

import (
	"errors"
	"time"
)

// LaneState represents the current state of a single lane in the ladder.
type LaneState struct {
	// Ceiling is the maximum quota allowed for this lane.
	Ceiling int
	// Cap indicates whether the lane has reached its ceiling.
	Cap bool
	// Expiry is the time when the current quota window expires.
	Expiry time.Time
	// Parked indicates whether the lane is temporarily inactive.
	Parked bool
	// Idle indicates whether the lane has no active work.
	Idle bool
}

// LadderPolicy holds the declarative data contract consumed by the ladder,
// including lane order, ceilings, caps, and window rules read from config.
type LadderPolicy struct {
	// LaneOrder defines the priority sequence of lanes.
	LaneOrder []string
	// Ceilings maps each lane identifier to its maximum quota ceiling.
	Ceilings map[string]int
	// WindowDuration is the duration of each quota window.
	WindowDuration time.Duration
}

// Config represents the configuration source for ReadPolicy.
type Config map[string]interface{}

// ReadPolicy reads a LadderPolicy from the provided config.
// It returns an error if required config keys are missing.
func ReadPolicy(cfg Config) (LadderPolicy, error) {
	if cfg == nil {
		return LadderPolicy{}, errors.New("config is nil")
	}

	orderRaw, ok := cfg["lane_order"]
	if !ok {
		return LadderPolicy{}, errors.New("missing required key: lane_order")
	}
	orderSlice, ok := orderRaw.([]string)
	if !ok {
		return LadderPolicy{}, errors.New("lane_order must be []string")
	}

	ceilingsRaw, ok := cfg["ceilings"]
	if !ok {
		return LadderPolicy{}, errors.New("missing required key: ceilings")
	}
	ceilingsMap, ok := ceilingsRaw.(map[string]int)
	if !ok {
		return LadderPolicy{}, errors.New("ceilings must be map[string]int")
	}

	windowRaw, ok := cfg["window_duration"]
	if !ok {
		return LadderPolicy{}, errors.New("missing required key: window_duration")
	}
	windowDur, ok := windowRaw.(time.Duration)
	if !ok {
		return LadderPolicy{}, errors.New("window_duration must be time.Duration")
	}

	return LadderPolicy{
		LaneOrder:      orderSlice,
		Ceilings:       ceilingsMap,
		WindowDuration: windowDur,
	}, nil
}

// SoonestExpiring returns the index of the quota expiring soonest among
// lanes that are not at cap and not parked. Returns -1 if none qualify.
func SoonestExpiring(lanes []LaneState) int {
	bestIdx := -1
	var bestTime time.Time

	for i, l := range lanes {
		if l.Cap || l.Parked {
			continue
		}
		if bestIdx == -1 || l.Expiry.Before(bestTime) {
			bestIdx = i
			bestTime = l.Expiry
		}
	}

	return bestIdx
}
