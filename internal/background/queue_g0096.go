package background

import (
	"errors"
	"strings"
)

// BriefTask represents a single task in a brief queue.
type BriefTask struct {
	ID     string
	Brief  string
	Status string
	Note   string
}

// BriefQueue represents a queue of brief tasks and its slot usage.
type BriefQueue struct {
	Tasks            []BriefTask
	StartSlotsUsed   int
	AccountSlotsUsed int
}

// BriefBuilder is the local contract for building a brief.
type BriefBuilder interface {
	Build(brief string) error
}

// ErrInvalidBriefQueue indicates that a brief queue or its components are invalid.
var ErrInvalidBriefQueue = errors.New("invalid brief queue")

// ErrBriefTaskNotFound indicates that a requested brief task could not be found.
var ErrBriefTaskNotFound = errors.New("brief task not found")

// isBlank reports whether a string is empty or contains only whitespace.
func isBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}

// validateBriefQueue validates a brief queue and its tasks. It ensures the queue ID is
// nonblank, the builder is not nil, counters are nonnegative, task IDs are unique and
// nonblank, briefs are nonblank, and statuses are exactly "queued" or "running".
// It returns ErrInvalidBriefQueue if validation fails. Nil or empty Tasks are valid.
func validateBriefQueue(q BriefQueue, id string, builder BriefBuilder) error {
	if isBlank(id) {
		return ErrInvalidBriefQueue
	}
	if builder == nil {
		return ErrInvalidBriefQueue
	}
	if q.StartSlotsUsed < 0 || q.AccountSlotsUsed < 0 {
		return ErrInvalidBriefQueue
	}

	seenIDs := make(map[string]struct{})
	for _, task := range q.Tasks {
		if isBlank(task.ID) {
			return ErrInvalidBriefQueue
		}
		if _, exists := seenIDs[task.ID]; exists {
			return ErrInvalidBriefQueue
		}
		seenIDs[task.ID] = struct{}{}

		if isBlank(task.Brief) {
			return ErrInvalidBriefQueue
		}

		if task.Status != "queued" && task.Status != "running" {
			return ErrInvalidBriefQueue
		}
	}

	return nil
}
