// Package promise defines promise types and an in-memory store for tracking
// asynchronous work with idempotent submission.
package promise

import "time"

// State represents the lifecycle state of a promise.
type State string

const (
	// Pending indicates the promise has been submitted but not yet started.
	Pending State = "pending"
	// Running indicates the promise is actively being processed.
	Running State = "running"
	// Done indicates the promise completed successfully.
	Done State = "done"
	// Failed indicates the promise failed after exhausting attempts.
	Failed State = "failed"
	// Expired indicates the promise exceeded its TTL without completion.
	Expired State = "expired"
	// Cancelled indicates the promise was explicitly cancelled.
	Cancelled State = "cancelled"
)

// Promise represents a unit of asynchronous work tracked by the store.
type Promise struct {
	// ID is the unique identifier for this promise.
	ID string
	// Kind categorizes the type of work this promise represents.
	Kind string
	// IdempotencyKey ensures duplicate submissions return the same promise.
	IdempotencyKey string
	// State is the current lifecycle state of the promise.
	State State
	// ResultRef holds a reference to the result once the promise completes.
	ResultRef string
	// Attempts tracks how many times processing has been attempted.
	Attempts int
	// NextAt indicates when the promise should next be eligible for processing.
	NextAt time.Time
	// TTL is the maximum duration the promise remains valid.
	TTL time.Duration
	// Heartbeat records the last time the promise was actively updated.
	Heartbeat time.Time
}
