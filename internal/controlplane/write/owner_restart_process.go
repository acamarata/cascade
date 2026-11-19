package write

import (
	"strings"
	"time"
)

// RecordProcessRestart records a process_restart event in the given AuditStore.
// It takes the store explicitly as *AuditStore and the clock value explicitly as at time.Time.
// It returns ErrInvalidRestart if s is nil, item is blank/whitespace-only, or at is zero.
func RecordProcessRestart(s *AuditStore, item string, at time.Time) error {
	if s == nil {
		return ErrInvalidRestart
	}
	if strings.TrimSpace(item) == "" {
		return ErrInvalidRestart
	}
	if at.IsZero() {
		return ErrInvalidRestart
	}
	return s.Append(AuditEvent{
		Kind: "process_restart",
		Item: item,
		At:   at,
	})
}
