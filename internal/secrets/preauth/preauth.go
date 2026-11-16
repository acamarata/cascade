// Package preauth provides local pre-authentication logic for request validation
// and rate limiting before signature verification.
package preauth

import (
	"errors"
	"strings"
	"time"
)

// Window is the default time window for rate limiting.
const Window time.Duration = time.Minute

// Sentinel errors for preauth operations.
var (
	// ErrInvalidConfig indicates that the PreAuth configuration is invalid.
	ErrInvalidConfig = errors.New("preauth: invalid config")
	// ErrInvalidRequest indicates that the request input is invalid.
	ErrInvalidRequest = errors.New("preauth: invalid request")
	// ErrInvalidSignature indicates that the request signature is invalid.
	ErrInvalidSignature = errors.New("preauth: invalid signature")
	// ErrRateLimited indicates that the request has been rate limited.
	ErrRateLimited = errors.New("preauth: rate limited")
)

// Request represents an incoming request that needs pre-authentication.
type Request struct {
	Source    string
	Body      []byte
	Signature []byte
}

// Verifier is a function that validates a request's signature.
type Verifier func(Request) error

// Clock is a function that returns the current time.
type Clock func() time.Time

// PreAuth handles pre-authentication logic including input validation
// and rate limiting.
type PreAuth struct {
	limit    int
	verifier Verifier
	now      Clock
}

// New creates a new PreAuth instance with the given limit, verifier, and clock.
// It returns an error if limit <= 0 or if verifier or clock are nil.
func New(limit int, verifier Verifier, now Clock) (*PreAuth, error) {
	if limit <= 0 || verifier == nil || now == nil {
		return nil, ErrInvalidConfig
	}
	return &PreAuth{
		limit:    limit,
		verifier: verifier,
		now:      now,
	}, nil
}

// Handle processes a request through pre-authentication checks.
// It returns an HTTP status code and an error.
// For nil or zero-value PreAuth, it returns 500 and ErrInvalidConfig.
// For invalid requests (empty/whitespace source, nil/empty body or signature),
// it returns 400 and ErrInvalidRequest without calling verifier or clock.
func (p *PreAuth) Handle(req Request) (int, error) {
	if p == nil || p.verifier == nil || p.now == nil {
		return 500, ErrInvalidConfig
	}

	// Validate Source: must not be empty or contain only Unicode whitespace
	if req.Source == "" || strings.TrimSpace(req.Source) == "" {
		return 400, ErrInvalidRequest
	}

	// Validate Body: must not be nil or empty
	if len(req.Body) == 0 {
		return 400, ErrInvalidRequest
	}

	// Validate Signature: must not be nil or empty
	if len(req.Signature) == 0 {
		return 400, ErrInvalidRequest
	}

	// Call the verifier for valid requests
	if err := p.verifier(req); err != nil {
		return 401, ErrInvalidSignature
	}

	return 200, nil
}
