package write

import (
	"errors"
	"strings"
)

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

// WriteScheduledIndexEvent writes a ScheduledIndexEvent to the given sink.
// It normalizes nil Names to a nonnil empty slice.
// It passes a copied Names slice to the sink to prevent mutation of the caller's input.
// If the sink returns an error, it is propagated unchanged without retry.
// It validates that Counts is non-negative, Counts matches len(Names), and names are unique,
// non-blank and contain no leading or trailing whitespace.
func WriteScheduledIndexEvent(sink EventSink, event ScheduledIndexEvent) error {
	if sink == nil {
		return ErrInvalidScheduledIndexEvent
	}
	if event.Counts < 0 {
		return ErrInvalidScheduledIndexEvent
	}

	// Normalize nil Names to a nonnil empty slice
	names := event.Names
	if names == nil {
		names = []string{}
	}

	if event.Counts != len(names) {
		return ErrInvalidScheduledIndexEvent
	}

	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || strings.TrimSpace(name) != name {
			return ErrInvalidScheduledIndexEvent
		}
		if seen[name] {
			return ErrInvalidScheduledIndexEvent
		}
		seen[name] = true
	}

	// Copy names slice to prevent sink mutation from affecting caller input
	copiedNames := make([]string, len(names))
	copy(copiedNames, names)

	copiedEvent := ScheduledIndexEvent{
		Counts: event.Counts,
		Names:  copiedNames,
	}

	return sink.InsertScheduledIndexEvent(copiedEvent)
}
