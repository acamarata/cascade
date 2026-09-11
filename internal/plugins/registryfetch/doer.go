package registryfetch

// Purpose: realDoer is the ONLY thing in this package that imports
//   "net/http" — the production Doer, backed by http.DefaultClient.
//   Isolating it here (rather than inlining it in fetch.go) keeps the
//   net/http import out of every _test.go file in this package (see
//   fetch.go's header comment on the no-network-unit-lane gate).
// Inputs: an HTTPRequest.
// Outputs: an HTTPResponse with the body already read and capped, or an
//   error.
// Constraints: this file carries no test double and is never itself
//   tested directly with a fake — fetch_test.go exercises it indirectly
//   through HTTPFetcher's real, default Doer only in the integration-
//   tagged file, if one is added later; the default unit lane covers
//   fetch.go's get() logic via fakeDoer instead (Art.2 — this file IS
//   the real counterpart fakeDoer stands in for).
// SPORT: internal/plugins/registryfetch (ADD) — P1-E24-W5-S50-T1.

import (
	"context"
	"io"
	"net/http"

	"github.com/acamarata/cascade/pkg/cascade"
)

// realDoer is the production Doer. Its Do method is intentionally thin:
// everything that does not require an actual socket (draining and
// capping a response body) lives in readHTTPResponse below, which takes
// a plain io.Reader rather than *http.Response so fetch_test.go can unit
// test it directly with a bytes.Reader or a failing io.Reader double —
// no net/http import required, and no real connection ever attempted in
// the default unit lane. Only request construction and the actual round
// trip stay in Do, since neither can be exercised without a socket or
// http/net types.
type realDoer struct{}

func (realDoer) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return HTTPResponse{}, cascade.Wrapf(cascade.KindInvalidInput, err, "registryfetch: build request for %s", req.URL)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	return readHTTPResponse(resp.StatusCode, resp.Body, req.URL)
}

// readHTTPResponse drains body (capped at maxResponseBytes) into an
// HTTPResponse. Split out of Do so it can be unit tested with any
// io.Reader, without importing net/http.
func readHTTPResponse(statusCode int, body io.Reader, url string) (HTTPResponse, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes))
	if err != nil {
		return HTTPResponse{}, cascade.Wrapf(cascade.KindUnavailable, err, "registryfetch: read body from %s", url)
	}
	return HTTPResponse{StatusCode: statusCode, Body: data}, nil
}
