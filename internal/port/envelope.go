package port

import "time"

// Request represents an incoming call envelope with routing and idempotency metadata.
type Request struct {
	// Version is the API version of the request.
	Version string
	// IdempotencyKey ensures safe retries without duplicate side effects.
	IdempotencyKey string
	// Caller identifies the originator of the request.
	Caller string
	// Deadline is the latest time by which the request must complete.
	Deadline time.Time
}

// Result represents the outcome of processing a Request.
// Status must be one of "ok", "partial", or "degraded".
type Result struct {
	// Status indicates the outcome state: "ok", "partial", or "degraded".
	Status string
	// AsOf records when the result was produced.
	AsOf time.Time
	// Missing lists any fields or resources that could not be resolved.
	// When Status is "degraded", Missing must list the degraded component names.
	Missing []string
}

// Validate checks that the Result satisfies its invariants.
// It returns ErrInvalid if Status is not "ok", "partial", or "degraded".
func (r Result) Validate() error {
	switch r.Status {
	case "ok", "partial", "degraded":
		return nil
	}
	return ErrInvalid
}
