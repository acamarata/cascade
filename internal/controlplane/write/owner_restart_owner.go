package write

import (
	"strings"
	"time"
)

// RecordOwnerRestart appends an owner_restart audit event for the given item and time.
// It rejects nil stores, blank or whitespace-padded items, and zero times with ErrInvalidRestart.
func RecordOwnerRestart(s *AuditStore, item string, at time.Time) error {
	if s == nil {
		return ErrInvalidRestart
	}
	trimmed := strings.TrimSpace(item)
	if trimmed == "" || trimmed != item {
		return ErrInvalidRestart
	}
	if at.IsZero() {
		return ErrInvalidRestart
	}
	return s.Append(AuditEvent{
		Kind: "owner_restart",
		Item: item,
		At:   at.UTC(),
	})
}
