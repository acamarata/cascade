// Package docket provides ticket event retention logic.
package docket

import (
	"errors"
	"time"
)

// ErrEventRetention is returned when retention validation fails.
var ErrEventRetention = errors.New("D4597")

// RetentionEvent represents a ticket event for retention trimming.
type RetentionEvent struct {
	TicketID string
	At       time.Time
	Kind     string
}

// validateEvents checks every event for required fields, future timestamps
// and duplicate (ticket, time, kind) tuples.
func validateEvents(events []RetentionEvent, now time.Time) error {
	type tuple struct {
		ticket string
		at     int64
		kind   string
	}

	seen := make(map[tuple]struct{})
	for _, e := range events {
		if e.TicketID == "" || e.Kind == "" {
			return ErrEventRetention
		}
		if e.At.IsZero() {
			return ErrEventRetention
		}
		if e.At.After(now) {
			return ErrEventRetention
		}
		t := tuple{
			ticket: e.TicketID,
			at:     e.At.UnixNano(),
			kind:   e.Kind,
		}
		if _, ok := seen[t]; ok {
			return ErrEventRetention
		}
		seen[t] = struct{}{}
	}
	return nil
}

// TrimEvents filters events based on a retention cutoff duration from now.
// It returns a fresh slice of retained events preserving input order.
// It returns ErrEventRetention if validation fails.
func TrimEvents(events []RetentionEvent, now time.Time, cutoff time.Duration) ([]RetentionEvent, error) {
	if cutoff < 0 {
		return nil, ErrEventRetention
	}
	if now.IsZero() {
		return nil, ErrEventRetention
	}

	if err := validateEvents(events, now); err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	threshold := now.Add(-cutoff)
	var result []RetentionEvent
	for _, e := range events {
		if !e.At.Before(threshold) {
			result = append(result, e)
		}
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}
