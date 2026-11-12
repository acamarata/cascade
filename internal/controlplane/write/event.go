// Package write provides functionality for writing control plane events.
package write

import "errors"

// ErrInvalidScheduledIndexEvent is returned when a ScheduledIndexEvent is invalid.
var ErrInvalidScheduledIndexEvent = errors.New("D5382: invalid scheduled index event")

// ScheduledIndexEvent represents an event containing counts and associated names.
type ScheduledIndexEvent struct {
	Counts int
	Names  []string
}

// EventSink defines the interface for persisting scheduled index events.
type EventSink interface {
	InsertScheduledIndexEvent(event ScheduledIndexEvent) error
}

// EmitScheduledIndexEvent writes a ScheduledIndexEvent to the given sink.
// It passes a copied Names slice to the sink to prevent mutation of the caller's input.
// If the sink returns an error, it is propagated unchanged without retry.
func EmitScheduledIndexEvent(sink EventSink, event ScheduledIndexEvent) error {
	if sink == nil {
		return ErrInvalidScheduledIndexEvent
	}

	// Copy names slice to prevent sink mutation from affecting caller input
	copiedNames := make([]string, len(event.Names))
	copy(copiedNames, event.Names)

	copiedEvent := ScheduledIndexEvent{
		Counts: event.Counts,
		Names:  copiedNames,
	}

	return sink.InsertScheduledIndexEvent(copiedEvent)
}
