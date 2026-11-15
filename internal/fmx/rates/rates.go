// Package rates implements rate tracking.
package rates

import (
	"errors"
	"time"
)

// Bucket represents a single time bucket with a start timestamp and an event count.
type Bucket struct {
	Start int64 `json:"start"`
	Count int   `json:"count"`
}

// Rates contains the tracked buckets for minute and hour intervals.
type Rates struct {
	Minutes []Bucket `json:"minutes"`
	Hours   []Bucket `json:"hours"`
}

// UpdateInterval is the fixed interval between rate updates.
const UpdateInterval = 15 * time.Second

// ErrRatesIO is returned when a rates I/O operation fails.
var ErrRatesIO = errors.New("D2533: rates I/O failed")

// ErrInvalidRates is returned when the parsed rates are invalid.
var ErrInvalidRates = errors.New("invalid rates")

// Tracker maintains the state for rate tracking.
type Tracker struct {
	path string
}

// NewTracker returns a new, non-nil, empty Tracker for the given rates.json path.
// It does not read from the disk, and restarts do not restore prior counts.
func NewTracker(path string) *Tracker {
	return &Tracker{
		path: path,
	}
}
