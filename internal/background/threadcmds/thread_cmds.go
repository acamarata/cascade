package threadcmds

import (
	"errors"
	"time"
)

// ErrNoThread is returned when an operation is attempted on an unknown thread.
var ErrNoThread = errors.New("no thread")

// ErrInvalidThreadInput is returned when a thread operation receives invalid input.
var ErrInvalidThreadInput = errors.New("invalid thread input")

// ThreadStore defines the interface for thread persistence and state management.
type ThreadStore interface {
	// Append atomically adds one message to the thread log or returns an error.
	Append(threadID, message string) error
	// Done returns a channel that closes when the thread is finished.
	Done(threadID string) (<-chan struct{}, error)
	// Result returns the result of the thread, whether it is done, and any error.
	Result(threadID string) (result string, done bool, err error)
}

// Clock defines the interface for time-based operations.
type Clock interface {
	// After returns a channel that receives the current time after the given duration.
	After(d time.Duration) <-chan time.Time
}

// ThreadCommands provides a logic API for thread delegation and messaging.
type ThreadCommands struct {
	store ThreadStore
	clock Clock
}

// NewThreadCommands creates a new ThreadCommands instance with the given dependencies.
// It returns ErrInvalidThreadInput if either store or clock is nil.
func NewThreadCommands(store ThreadStore, clock Clock) (*ThreadCommands, error) {
	if store == nil || clock == nil {
		return nil, ErrInvalidThreadInput
	}
	return &ThreadCommands{
		store: store,
		clock: clock,
	}, nil
}

// Send appends a message to the specified thread's log.
// It returns ErrInvalidThreadInput if the threadID or message is empty.
// Whitespace is considered valid literal content.
// Send propagates any errors returned by the underlying store's Append method.
func (c *ThreadCommands) Send(threadID, message string) error {
	if threadID == "" || message == "" {
		return ErrInvalidThreadInput
	}
	return c.store.Append(threadID, message)
}
