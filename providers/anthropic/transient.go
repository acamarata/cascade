// Purpose: transient marking for the anthropic driver (P1-GWY-03,
//   contract:transient-error-class): HTTP status mapping (moved here from
//   stream.go), the SSE error event and transport failures, each marked
//   with provider.MarkTransient when the upstream failure is worth a retry.
// Inputs: a status and body, a decoded SSE error envelope, or a transport
//   error with the request's context.
// Outputs: a taxonomy error, marked transient where the shape says so.
// Constraints: classification uses the status, the error type and the
//   transport outcome only, never message text. A 4xx is never transient
//   (429 belongs to the quota spill); neither is a cancelled or expired
//   context. The driver never retries; the conductor does.
// SPORT: placeholder: providers/anthropic driver (CHANGE, P1-GWY-03).

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Statuses the transient rule names. 501 and 505 describe a request the
// upstream will never serve, so they are not transient.
const (
	statusNotImplemented          = 501
	statusHTTPVersionNotSupported = 505
	statusServerErrorCeiling      = 600
)

// Error types of the Messages API error envelope the rule reads.
const (
	errTypeAPI        = "api_error"
	errTypeOverloaded = "overloaded_error"
)

// mapStatusError classifies an HTTP error response by status code, per
// 06 §5's explicit-mapping requirement (429 and the 529 overload status
// named specifically), and marks the transient ones.
func mapStatusError(status int, body []byte) error {
	msg := errorMessage(status, body)
	switch status {
	case statusBadRequest, statusRequestTooLarge:
		return cascade.New(cascade.KindInvalidInput, msg)
	case statusUnauthorized, statusForbidden:
		return cascade.New(cascade.KindPermissionDenied, msg)
	case statusNotFound:
		return cascade.New(cascade.KindNotFound, msg)
	case statusTooManyRequests:
		return cascade.New(cascade.KindQuotaExhausted, msg)
	case statusOverloaded:
		return markStatus(cascade.New(cascade.KindUnavailable, msg), status, body)
	default:
		if status >= statusInternalServerError {
			return markStatus(cascade.New(cascade.KindUnavailable, msg), status, body)
		}
		return cascade.New(cascade.KindInternal, msg)
	}
}

// markStatus marks err transient when status is a retryable 5xx: the
// overload status or an overloaded_error body is "overloaded", any other
// 5xx except 501 and 505 is "http_5xx".
func markStatus(err error, status int, body []byte) error {
	if status < statusInternalServerError || status >= statusServerErrorCeiling ||
		status == statusNotImplemented || status == statusHTTPVersionNotSupported {
		return err
	}
	cause := provider.TransientHTTP5xx
	if status == statusOverloaded || errorType(body) == errTypeOverloaded {
		cause = provider.TransientOverloaded
	}
	return provider.MarkTransient(err, provider.TransientInfo{Cause: cause, Status: status})
}

// errorType reads error.type from a Messages API error envelope, or "".
func errorType(body []byte) string {
	var wireErr wireErrorBody
	if json.Unmarshal(body, &wireErr) != nil {
		return ""
	}
	return wireErr.Error.Type
}

// streamAPIError builds the error an SSE error event carries. An api_error
// or overloaded_error event is marked "stream_api_error"; the conductor
// retries it only while no output has reached the caller.
func streamAPIError(wireErr wireErrorBody) error {
	err := cascade.Newf(cascade.KindUnavailable, "anthropic: stream error %s: %s", wireErr.Error.Type, wireErr.Error.Message)
	if t := wireErr.Error.Type; t == errTypeAPI || t == errTypeOverloaded {
		return provider.MarkTransient(err, provider.TransientInfo{Cause: provider.TransientStreamAPIError})
	}
	return err
}

// markTransport marks a transport failure transient when the connection
// was reset or closed early (EOF) while ctx was still live. A failure the
// caller's own cancellation or deadline caused is never transient.
func markTransport(ctx context.Context, mapped, raw error) error {
	if ctx.Err() != nil || !isConnectionReset(raw) {
		return mapped
	}
	return provider.MarkTransient(mapped, provider.TransientInfo{Cause: provider.TransientTransport})
}

// isConnectionReset reports a reset, abort, broken pipe or early EOF.
func isConnectionReset(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE)
}

// readBodyError maps a failure reading a response body, marking a reset or
// early EOF with a live ctx as a transport failure.
func readBodyError(ctx context.Context, err error) error {
	return markTransport(ctx, cascade.Wrap(cascade.KindUnavailable, err, "anthropic: reading response body"), err)
}
