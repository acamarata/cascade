package write

import (
	"errors"
	"strings"
	"time"
)

// ErrInvalidRestart is returned when an audit event fails validation.
var ErrInvalidRestart = errors.New("invalid restart event")

// AuditEvent represents an audit event with kind, item, and timestamp.
type AuditEvent struct {
	Kind string
	Item string
	At   time.Time
}

// AuditStore is an in-memory audit store that maintains insertion order.
// A zero-value AuditStore is ready to use.
type AuditStore struct {
	events []AuditEvent
}

// Append adds an audit event to the store after validation.
// It rejects nil store, blank/whitespace-only or whitespace-padded Item,
// zero At timestamp, and any Kind other than "owner_restart" or "process_restart".
// Valid timestamps are stored as UTC with nanosecond precision preserved.
// The event value is copied and insertion order is maintained.
func (s *AuditStore) Append(e AuditEvent) error {
	if s == nil {
		return ErrInvalidRestart
	}

	// Validate Kind
	if e.Kind != "owner_restart" && e.Kind != "process_restart" {
		return ErrInvalidRestart
	}

	// Validate Item (blank/whitespace-only or whitespace-padded)
	if strings.TrimSpace(e.Item) == "" || e.Item != strings.TrimSpace(e.Item) {
		return ErrInvalidRestart
	}

	// Validate At (zero timestamp)
	if e.At.IsZero() {
		return ErrInvalidRestart
	}

	// Copy the event and convert timestamp to UTC
	storedEvent := AuditEvent{
		Kind: e.Kind,
		Item: e.Item,
		At:   e.At.UTC(),
	}

	s.events = append(s.events, storedEvent)
	return nil
}
