// Package harx provides event watching utilities.
package harx

import (
	"context"
	"errors"
	"math"
)

// Event represents a single watched event.
type Event struct {
	// ID is the unique identifier of the event.
	ID int64
	// Data holds the event payload.
	Data string
}

// Source is an interface for polling or streaming events.
type Source interface {
	// Poll returns events with IDs strictly greater than after.
	Poll(after int64) ([]Event, error)
	// Stream sends events to sink until ctx is cancelled or an error occurs.
	Stream(ctx context.Context, sink func(Event)) error
}

// ErrWatchBadAfter is returned when ParseAfter receives an invalid cursor string.
var ErrWatchBadAfter = errors.New("WATCH_BAD_AFTER")

// ParseAfter parses s as a non-negative decimal int64 cursor.
// An empty string returns (0, nil). Signs, whitespace, non-ASCII digits,
// and values exceeding math.MaxInt64 return (0, ErrWatchBadAfter).
func ParseAfter(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, ErrWatchBadAfter
		}
		d := int64(c - '0')
		if n > (math.MaxInt64-d)/10 {
			return 0, ErrWatchBadAfter
		}
		n = n*10 + d
	}
	return n, nil
}
