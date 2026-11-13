package promise

import (
	"context"
	"errors"
	"time"
)

// ErrNotImplemented is the sentinel error returned when an operation is unavailable.
var ErrNotImplemented = errors.New("promise: operation unavailable")

const (
	// defaultMaxAttempts is the default retry limit for promises.
	defaultMaxAttempts = 3

	// defaultBackoff is the default delay between retries.
	defaultBackoff = 30 * time.Second
)

// RetryPolicy configures the retry behaviour for a promise.
type RetryPolicy struct {
	// MaxAttempts is the maximum number of execution attempts.
	MaxAttempts int
	// Backoff is the delay between consecutive attempts.
	Backoff time.Duration
}

// Await blocks until the promise identified by id reaches a terminal state
// or the timeout expires. It currently returns an error indicating the
// operation is unavailable.
func Await(_ context.Context, _ string, _ time.Duration) (*Promise, error) {
	return nil, ErrNotImplemented
}

// RecoverStale searches the store for promises that are stale and attempts
// to recover them. It currently returns an error indicating the operation
// is unavailable.
func RecoverStale(_ context.Context, _ Store) error {
	return ErrNotImplemented
}

// Do executes the given promise using the supplied store and timeout.
// It currently returns an error indicating the operation is unavailable.
func Do(_ context.Context, _ Store, _ *Promise, _ time.Duration) (*Promise, error) {
	return nil, ErrNotImplemented
}
