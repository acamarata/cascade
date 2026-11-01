// Purpose: the transient-error marker (contract:transient-error-class,
//   P1-GWY-03): a driver marks an upstream failure worth retrying, with
//   its cause, status and any Retry-After hint, and the conductor reads
//   the mark back. Marking never changes the error a caller sees.
// Inputs: an error a driver produced, plus TransientInfo.
// Outputs: the same error, carrying the mark.
// Constraints: the cause set is closed. A driver classifies by status,
//   error type and transport outcome only, never by message text. The
//   conductor alone decides whether a marked failure is retried.
// SPORT: pkg.provider.transient-error-class/ADD (P1-GWY-03).

package provider

import (
	"errors"
	"time"
)

// TransientCause names why a failure is transient. The set is closed: Valid
// reports membership.
type TransientCause string

// The closed set of transient causes.
const (
	// TransientHTTP5xx is a 5xx response other than 501 and 505.
	TransientHTTP5xx TransientCause = "http_5xx"
	// TransientOverloaded is the overload status (529) or an error body of
	// type overloaded_error.
	TransientOverloaded TransientCause = "overloaded"
	// TransientStreamAPIError is an SSE error event of type api_error or
	// overloaded_error.
	TransientStreamAPIError TransientCause = "stream_api_error"
	// TransientTransport is a connection reset or EOF while the caller's
	// context was still live.
	TransientTransport TransientCause = "transport"
)

// Valid reports whether c is a member of the closed cause set.
func (c TransientCause) Valid() bool {
	switch c {
	case TransientHTTP5xx, TransientOverloaded, TransientStreamAPIError, TransientTransport:
		return true
	default:
		return false
	}
}

// TransientInfo describes one transient failure.
type TransientInfo struct {
	// Cause is why the failure is transient.
	Cause TransientCause
	// Status is the HTTP status, or 0 when no response arrived.
	Status int
	// RetryAfter is the upstream's retry hint, or 0 when it gave none.
	RetryAfter time.Duration
}

// transientError carries the mark. It reports its inner error's text and
// unwraps to it, so the Kind, the wire code and the message are unchanged.
type transientError struct {
	err  error
	info TransientInfo
}

// Error returns the inner error's text unchanged.
func (t *transientError) Error() string { return t.err.Error() }

// Unwrap returns the inner error, so its Kind and code stay reachable.
func (t *transientError) Unwrap() error { return t.err }

// MarkTransient returns err carrying info. A nil err stays nil. The result
// keeps err's cascade Kind, code and message; errors.As and errors.Unwrap
// reach err through it, and TransientOf reads info back through any
// number of wrapping layers.
func MarkTransient(err error, info TransientInfo) error {
	if err == nil {
		return nil
	}
	return &transientError{err: err, info: info}
}

// TransientOf reports the mark err's chain carries, if any.
func TransientOf(err error) (TransientInfo, bool) {
	var t *transientError
	if errors.As(err, &t) {
		return t.info, true
	}
	return TransientInfo{}, false
}
