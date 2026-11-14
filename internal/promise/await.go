package promise

import (
	"time"
)

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
