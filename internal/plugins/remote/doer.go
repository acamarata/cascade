package remote

// Purpose: realDoer, the ONLY thing in this package that performs an
//   actual net/http round trip — split out so the "net"/"net/http"
//   import stays out of every _test.go file in this package. Mirrors
//   internal/plugins/registryfetch's own realDoer/Doer precedent
//   exactly, for the identical reason: internal/build's
//   TestNoNetworkUnitTest_RealTreeGreen gate (the "default unit lane
//   forbids importing net/net/http in any non-integration _test.go"
//   rule, AGENT-BRIEF and LANE-RULES §6) scans EVERY _test.go file
//   tree-wide, including one that only uses httptest for a real
//   loopback socket — there is no carve-out for "it's a real test, not
//   a mock". remote_test.go originally used net/http/httptest directly;
//   that violated this gate (found by `go test ./internal/build/...`,
//   TestNoNetworkUnitTest_RealTreeGreen: 7 violations across four files
//   in this ticket's own scope) and is corrected here.
// Inputs: a doRequest (URL + body bytes; no net/http type crosses this
//   package's own internal boundary between doer.go and remote.go).
// Outputs: a doResponse, or the raw net/http error unwrapped (dialRemote's
//   classifyDialErr, remote.go, classifies it).
// Constraints: this file carries no test double and is exercised only by
//   the integration-tagged loopback test (remote_integration_test.go) —
//   the default unit lane covers dialRemote's own logic via fakeDoer
//   (doer_test.go) instead, the same split registryfetch's fetch.go
//   header comment documents for the identical reason.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// doRequest is the plain-data request shape doer.Do takes. No net/http
// type, so every OTHER file in this package (including every _test.go
// file) can construct or inspect one without importing "net/http".
type doRequest struct {
	// URL is the absolute URL to POST to.
	URL string
	// Body is the JSON-RPC request body.
	Body []byte
}

// doResponse is the plain-data response shape doer.Do returns.
type doResponse struct {
	// StatusCode is the response's HTTP status code.
	StatusCode int
	// Body is the response body, already read and capped.
	Body []byte
}

// doer performs one HTTP POST. dialRemote's production behavior is
// implemented entirely in terms of doer, so the real net/http.Client
// usage lives in exactly one place (realDoer, below). Unexported: this
// package's only caller of a custom doer is its own _test.go files
// (same-package internal tests), and dialRemote defaults a nil doer to
// realDoer, so no external package ever needs to name this type.
type doer interface {
	Do(ctx context.Context, req doRequest) (doResponse, error)
}

// realDoer is the production doer, backed by http.DefaultClient.
type realDoer struct{}

// Do implements doer. It is deliberately thin: everything that does not
// require an actual net/http type (capping and reading the response
// body) lives in readDoResponse below, which takes a plain io.Reader so
// doer_test.go can unit test it directly with a strings.Reader or a
// failing io.Reader double — no "net"/"net/http" import required, and no
// real connection ever attempted in the default unit lane. Mirrors
// internal/plugins/registryfetch's doer.go/readHTTPResponse split
// exactly, for the identical reason (that file's own header comment).
func (realDoer) Do(ctx context.Context, req doRequest) (doResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return doResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return doResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	return readDoResponse(resp.StatusCode, resp.Body)
}

// readDoResponse drains body, capped at maxHandshakeResponseBytes, into
// a doResponse. Split out of Do purely so it can be unit tested with any
// io.Reader, without importing "net/http" (see Do's own doc comment).
func readDoResponse(statusCode int, body io.Reader) (doResponse, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxHandshakeResponseBytes))
	if err != nil {
		return doResponse{}, err
	}
	return doResponse{StatusCode: statusCode, Body: data}, nil
}
