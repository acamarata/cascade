// Package background provides local token-bucket rate limiting.
package background

import (
	"errors"
	"time"
)

// ErrRateLimited is returned when a request exceeds the bucket capacity.
var ErrRateLimited = errors.New("E_RATE_LIMITED")

// ErrInvalidCapacity is returned when a bucket is constructed with non-positive capacity.
var ErrInvalidCapacity = errors.New("E_INVALID_CAPACITY")

// ErrInvalidStreamInput is returned when a bucket is constructed with non-positive rate.
var ErrInvalidStreamInput = errors.New("invalid stream input")

// StreamBucket holds token-bucket state for a single stream.
type StreamBucket struct {
	// Capacity is the maximum number of tokens the bucket can hold.
	Capacity int
	// Rate is the number of tokens added per second.
	Rate int
	// Tokens is the current number of available tokens.
	Tokens int
	// Last records the time at which the bucket was last updated or initialized.
	Last time.Time
	// Initialized indicates whether the bucket has been properly set up.
	Initialized bool
}

// NewStreamBucket creates a full StreamBucket with the given capacity and rate.
// Capacity must be greater than zero; otherwise a zero bucket and ErrInvalidCapacity are returned.
// Rate must be greater than zero; otherwise a zero bucket and ErrInvalidStreamInput are returned.
func NewStreamBucket(capacity, rate int, now time.Time) (StreamBucket, error) {
	if capacity <= 0 {
		return StreamBucket{}, ErrInvalidCapacity
	}
	if rate <= 0 {
		return StreamBucket{}, ErrInvalidStreamInput
	}
	return StreamBucket{
		Capacity:    capacity,
		Rate:        rate,
		Tokens:      capacity,
		Last:        now,
		Initialized: true,
	}, nil
}
