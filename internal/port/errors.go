package port

import "errors"

// PortError is the interface for all structured errors returned by port operations.
type PortError interface { //nolint:revive // name fixed by the ticket contract
	error
	// Code returns a machine-readable error code.
	Code() string
	// Retryable reports whether the operation may succeed if retried.
	Retryable() bool
}

// ErrNotImplemented is returned when an operation has no implementation yet.
var ErrNotImplemented error = errors.New("not implemented")

// portError is the internal stub implementation of PortError.
type portError struct {
	msg string
}

// Error returns the error message.
func (e *portError) Error() string { return e.msg }

// Code returns an empty string as a stub.
func (e *portError) Code() string { return "" }

// Retryable returns false as a stub.
func (e *portError) Retryable() bool { return false }

// Sentinel errors for standard failure conditions.
var (
	// ErrInvalid indicates the request was malformed or failed validation.
	ErrInvalid PortError = &portError{msg: "invalid"}
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound PortError = &portError{msg: "not found"}
	// ErrConflict indicates the request conflicts with current state.
	ErrConflict PortError = &portError{msg: "conflict"}
	// ErrDenied indicates the caller lacks permission.
	ErrDenied PortError = &portError{msg: "denied"}
	// ErrUnavailable indicates the service is temporarily unavailable.
	ErrUnavailable PortError = &portError{msg: "unavailable"}
	// ErrTimeout indicates the operation exceeded its deadline.
	ErrTimeout PortError = &portError{msg: "timeout"}
	// ErrExhausted indicates a rate limit or quota has been reached.
	ErrExhausted PortError = &portError{msg: "exhausted"}
	// ErrInternal indicates an unexpected internal failure.
	ErrInternal PortError = &portError{msg: "internal"}
)
