package port

// PortError is the interface for all structured errors returned by port
// operations. The set of values is closed: only the eight sentinels below
// exist, so a caller can switch on Code and decide on Retryable alone.
type PortError interface { //nolint:revive // name fixed by the ticket contract
	error
	// Code returns the machine-readable P1 taxonomy code.
	Code() string
	// Retryable reports whether the operation may succeed if retried.
	Retryable() bool
}

// portError is the only PortError implementation. It holds a fixed code
// and message, never a wrapped cause, secret or stack, so it is safe to
// send across any boundary.
type portError struct {
	code      string
	retryable bool
	msg       string
}

// newPortError binds a taxonomy code to its retryability.
func newPortError(code string, retryable bool) *portError {
	return &portError{code: code, retryable: retryable, msg: "port: " + code}
}

// Error returns the fixed one-line message.
func (e *portError) Error() string { return e.msg }

// Code returns the taxonomy code.
func (e *portError) Code() string { return e.code }

// Retryable reports whether a retry may succeed.
func (e *portError) Retryable() bool { return e.retryable }

// Sentinel errors bound to the P1 taxonomy (capp counterpart: proto errors).
var (
	// ErrInvalid indicates the request was malformed or failed validation.
	ErrInvalid PortError = newPortError("invalid", false)
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound PortError = newPortError("not_found", false)
	// ErrConflict indicates the request conflicts with current state.
	ErrConflict PortError = newPortError("conflict", false)
	// ErrDenied indicates the caller lacks permission.
	ErrDenied PortError = newPortError("denied", false)
	// ErrUnavailable indicates the service is temporarily unavailable.
	ErrUnavailable PortError = newPortError("unavailable", true)
	// ErrTimeout indicates the operation exceeded its deadline.
	ErrTimeout PortError = newPortError("timeout", true)
	// ErrExhausted indicates a rate limit or quota has been reached.
	ErrExhausted PortError = newPortError("exhausted", true)
	// ErrInternal indicates an unexpected internal failure.
	ErrInternal PortError = newPortError("internal", false)
)
