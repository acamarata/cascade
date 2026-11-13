// Package background provides batch processing utilities.
package background

import "strings"

// BatchEvent represents a single event in a batch to be processed.
type BatchEvent struct {
	ID      string
	Leading bool
}

// JournalEntry represents the result of processing a single BatchEvent.
type JournalEntry struct {
	ID      string `json:"id"`
	Covered bool   `json:"covered"`
}

// Quota tracks hourly limits that are incremented by leading events.
type Quota struct {
	PerHour int
}

// ProcessBatch validates and processes a slice of BatchEvents, returning
// corresponding JournalEntries. A nil or empty events slice is valid and
// returns an empty non-nil slice without modifying the quota. Each entry's
// Covered field is set to !event.Leading. The number of leading events is
// added to q.PerHour only after the entire batch passes validation.
// Duplicate or blank/whitespace-only IDs cause ErrInvalidBatch with no
// quota change.
func ProcessBatch(events []BatchEvent, q *Quota) ([]JournalEntry, error) {
	if q == nil || q.PerHour < 0 {
		return nil, ErrInvalidBatch
	}

	entries := make([]JournalEntry, 0, len(events))
	if len(events) == 0 {
		return entries, nil
	}

	seen := make(map[string]struct{}, len(events))
	leadingCount := 0

	for _, ev := range events {
		if strings.TrimSpace(ev.ID) == "" {
			return nil, ErrInvalidBatch
		}
		if _, exists := seen[ev.ID]; exists {
			return nil, ErrInvalidBatch
		}
		seen[ev.ID] = struct{}{}

		entries = append(entries, JournalEntry{
			ID:      ev.ID,
			Covered: !ev.Leading,
		})

		if ev.Leading {
			leadingCount++
		}
	}

	q.PerHour += leadingCount

	return entries, nil
}
