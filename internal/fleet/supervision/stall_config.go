package supervision

// Purpose (this file): ParseStallThreshold, the [fleet.supervision]
// stall_threshold reader for the composition root.
//
// Inputs: the decoded [fleet.supervision] table (a plain map).
// Outputs: the idle/stall cutoff duration.
// Constraints: absent means the 30m default; a value that is not a
// duration string, is under 1m or over 24h is KindInvalidInput.
//
// SPORT: fleet.supervision.stall-rungs/ADDED (P1-SUP-03).

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	stallThresholdKey     = "stall_threshold"
	defaultStallThreshold = 30 * time.Minute
	minStallThreshold     = time.Minute
	maxStallThreshold     = 24 * time.Hour
)

// ParseStallThreshold reads [fleet.supervision].stall_threshold from extra.
func ParseStallThreshold(extra map[string]interface{}) (time.Duration, error) {
	raw, present := extra[stallThresholdKey]
	if !present {
		return defaultStallThreshold, nil
	}
	text, ok := raw.(string)
	if !ok {
		return 0, cascade.New(cascade.KindInvalidInput, "supervision: stall_threshold must be a duration string")
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInvalidInput, err, "supervision: stall_threshold is not a valid duration")
	}
	if d < minStallThreshold || d > maxStallThreshold {
		return 0, cascade.Newf(cascade.KindInvalidInput, "supervision: stall_threshold %s is outside 1m..24h", d)
	}
	return d, nil
}
