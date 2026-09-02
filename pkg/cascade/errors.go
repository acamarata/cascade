// Package cascade holds the single error taxonomy for the whole cascade
// binary (T0 ruling R-14.2): the closed set of error Kinds, their stable
// wire codes (CLI exit status, JSON-RPC error code, plugin RPC error code —
// see codes.go and wire.go), and the constructors and inspection helpers
// call sites use to produce and recognize taxonomy errors.
//
// Call sites read cascade.ErrNotFound and cascade.Kind, never a
// stdlib-shadowing "errors" package name — hence this package is named
// cascade, not errors.
//
// pkg/cascade ships the kinds, codes, constructors, and inspection helpers
// ONLY. Domain-specific sentinel error values (for example storage's
// ErrDomainOwned) belong in their OWNING package, each wrapping exactly one
// frozen Kind from this package — they are never added here. This keeps the
// taxonomy consumable by downstream packages (B/S-02.T1, D/S-06.T3) with
// zero additions.
//
// Every exported API surface (pkg/, cmd/ composition) must return an *Error
// from this package rather than a raw fmt.Errorf/errors.New value; the
// boundary lint in internal/build enforces this at the pkg/ and cmd/
// boundary. internal/ packages remain free to wrap however they like.
package cascade

import (
	"errors"
	"fmt"
)

// Error is the taxonomy's single error type: a Kind plus a human-readable
// message and an optional wrapped inner error. Every taxonomy error —
// whether produced by New, Newf, Wrap, Wrapf, or one of the per-kind
// sentinels — is an *Error, so callers can always recover the Kind via
// KindOf or errors.As.
type Error struct {
	// Kind is the taxonomy member this error belongs to. Always Valid() for
	// an *Error constructed through this package's own constructors.
	Kind Kind
	// Msg is the human-readable message. May be empty, in which case
	// Error() falls back to the Kind's String().
	Msg string
	// Err is the wrapped inner error, or nil. Unwrap exposes it so
	// errors.Is/errors.As traverse through it.
	Err error
}

// Error implements the error interface. The format is "<kind>: <msg>", or
// "<kind>: <msg>: <wrapped>" when Err is set, or just "<kind>" when Msg is
// empty.
func (e *Error) Error() string {
	switch {
	case e.Msg == "" && e.Err == nil:
		return e.Kind.String()
	case e.Msg == "" && e.Err != nil:
		return fmt.Sprintf("%s: %v", e.Kind, e.Err)
	case e.Err == nil:
		return fmt.Sprintf("%s: %s", e.Kind, e.Msg)
	default:
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Msg, e.Err)
	}
}

// Unwrap exposes the wrapped inner error so errors.Is and errors.As traverse
// through it, per the standard library's error-chain protocol.
func (e *Error) Unwrap() error {
	return e.Err
}

// Is implements the errors.Is interop hook. Two taxonomy errors are
// considered equivalent when they share a Kind, regardless of message or
// wrapped error — this is what lets a call site write
// errors.Is(err, cascade.ErrNotFound) and get a true regardless of which
// constructor or message produced err, and what lets an internal package
// wrap a taxonomy error without the caller losing the ability to test its
// Kind.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Kind == t.Kind
}

// New constructs a taxonomy error of the given kind with the given message.
func New(kind Kind, msg string) *Error {
	return &Error{Kind: kind, Msg: msg}
}

// Newf constructs a taxonomy error of the given kind with a formatted
// message, in the manner of fmt.Sprintf.
func Newf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Wrap constructs a taxonomy error of the given kind that wraps err. The
// original err remains reachable via errors.Unwrap/errors.As, so callers
// that need the underlying cause (e.g. for logging) can still get at it
// while the Kind stays authoritative for control flow.
func Wrap(kind Kind, err error, msg string) *Error {
	return &Error{Kind: kind, Msg: msg, Err: err}
}

// Wrapf is Wrap with a formatted message, in the manner of fmt.Sprintf.
func Wrapf(kind Kind, err error, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...), Err: err}
}

// KindOf reports the Kind carried by err's error chain, if any. It walks the
// chain with errors.As, so a taxonomy error wrapped by a stdlib
// fmt.Errorf("...: %w", err) (as internal/ packages are free to do) is still
// found. ok is false when no *Error appears anywhere in the chain,
// including when err is nil.
func KindOf(err error) (kind Kind, ok bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind, true
	}
	return 0, false
}

// HasKind reports whether err's error chain carries the given Kind. It is a
// convenience wrapper over KindOf for the common case of testing a single
// kind.
func HasKind(err error, kind Kind) bool {
	k, ok := KindOf(err)
	return ok && k == kind
}

// Per-kind sentinel errors. Each wraps no message and no inner error; they
// exist so call sites can write errors.Is(err, cascade.ErrNotFound) without
// constructing their own comparison value. Because *Error.Is compares only
// on Kind, any taxonomy error of the matching kind — constructed by New,
// Newf, Wrap, Wrapf, or a domain-specific sentinel that wraps this Kind —
// satisfies errors.Is against the corresponding sentinel below.
var (
	ErrNotFound          = &Error{Kind: KindNotFound}
	ErrInvalidInput      = &Error{Kind: KindInvalidInput}
	ErrConflict          = &Error{Kind: KindConflict}
	ErrUnavailable       = &Error{Kind: KindUnavailable}
	ErrTimeout           = &Error{Kind: KindTimeout}
	ErrCanceled          = &Error{Kind: KindCanceled}
	ErrPermissionDenied  = &Error{Kind: KindPermissionDenied}
	ErrElevationRequired = &Error{Kind: KindElevationRequired}
	ErrPolicyDenied      = &Error{Kind: KindPolicyDenied}
	ErrCapabilityDenied  = &Error{Kind: KindCapabilityDenied}
	ErrQuotaExhausted    = &Error{Kind: KindQuotaExhausted}
	ErrUnsupported       = &Error{Kind: KindUnsupported}
	ErrIntegrity         = &Error{Kind: KindIntegrity}
	ErrInternal          = &Error{Kind: KindInternal}
)
