// Package background provides retry journaling primitives.
package background

import (
	"errors"
	"fmt"
)

// ErrInvalidRetryEntry signals an invalid retry entry (code D0294).
var ErrInvalidRetryEntry = errors.New("D0294")

// ErrRetryJournal signals a journal append failure.
var ErrRetryJournal = errors.New("retry journal failed")

// ErrDuplicateRetryEntry signals a duplicate retry entry.
var ErrDuplicateRetryEntry = errors.New("duplicate retry entry")

// Kind represents the category of a retry event.
type Kind string

const (
	// KindCallFailed indicates a call failure.
	KindCallFailed Kind = "call_failed"
	// KindOpenerFailed indicates an opener failure.
	KindOpenerFailed Kind = "opener_failed"
	// KindHourlyCap indicates an hourly cap refusal.
	KindHourlyCap Kind = "hourly_cap"
)

// RetryEntry holds data for a single retry event.
type RetryEntry struct {
	ID     string
	Kind   Kind
	Detail string
}

// RetryJournal accepts retry entries.
type RetryJournal interface {
	AppendRetry(entry RetryEntry) error
}

// RetryVerdictSink writes verdicts for retry entries.
type RetryVerdictSink interface {
	WriteVerdict(entry RetryEntry) error
}

// JournalRetry validates and records a retry entry into the journal.
func JournalRetry(entry RetryEntry, journal RetryJournal, _ RetryVerdictSink) error {
	if journal == nil {
		return ErrInvalidRetryEntry
	}
	if entry.ID == "" || entry.Detail == "" {
		return ErrInvalidRetryEntry
	}
	switch entry.Kind {
	case KindCallFailed, KindOpenerFailed, KindHourlyCap:
	default:
		return ErrInvalidRetryEntry
	}
	if err := journal.AppendRetry(entry); err != nil {
		return fmt.Errorf("%w: %w", ErrRetryJournal, err)
	}
	return nil
}
