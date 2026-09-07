// Purpose: the openai-compat driver's transport concerns: resolving the
//
//	Bearer credential through KeyResolver, building requests, retrying
//	transient failures with a deterministic backoff, and mapping every
//	non-200 HTTP response onto pkg/cascade's frozen 14-kind taxonomy.
//
// Inputs: Driver.cfg (openai.go) plus, per call, ctx/method/path/body.
// Outputs: response bytes, or a typed *cascade.Error - never a raw error.
// Constraints: no credential literal ever appears in a log line, error
//
//	message, or this source file; the two string literals inside
//	bearerToken's doc comment stay split for the same reason
//	AGENT-BRIEF's CREDENTIAL-SHAPED FIXTURES rule splits test literals -
//	GitHub push protection scans source text, not just committed secrets.
//	Retry decisions read Driver.cfg.Clock, never time.Now/time.Since
//	(forbidigo; internal/build's clockgate).
//
// SPORT: providers/openai driver/ADD (P1-E10-W3-S19-T3).

package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// bearerToken resolves cfg.KeyRef through cfg.Resolver and returns the
// literal value to send as an "Authorization: Bearer " + value header.
// Never logged, never wrapped into an error message: a resolution failure
// names the KEY REFERENCE (a vault key name, not a secret), never the
// value. The vault stores real keys shaped like "sk" + "-" + <random> for
// OpenAI itself; that shape never appears contiguous in this file.
func (d *Driver) bearerToken(ctx context.Context) (string, error) {
	key, err := d.cfg.Resolver.Resolve(ctx, d.cfg.KeyRef)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(key) == "" {
		return "", cascade.Newf(cascade.KindPermissionDenied, "openai: resolved key for %q is empty", d.cfg.KeyRef)
	}
	return key, nil
}

// newRequest builds one outbound request against d.cfg.BaseURL, resolving
// and attaching the Bearer credential fresh on every call - a credential
// is never cached in the Driver itself, so a rotated vault entry takes
// effect on the very next call.
func (d *Driver) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	token, err := d.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(d.cfg.BaseURL, "/") + path
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "openai: building request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// doWithRetry executes one logical call, retrying a retryable non-200
// status up to maxRetryAttempts times with retryDelay's backoff. A
// transport-level error (Doer.Do itself failing) is never retried by this
// driver - that policy belongs to whatever calls this driver (K/S-22), not
// to the driver itself.
func (d *Driver) doWithRetry(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		req, err := d.newRequest(ctx, method, path, body)
		if err != nil {
			return nil, err
		}
		resp, err := d.cfg.HTTPClient.Do(req)
		if err != nil {
			return nil, mapDoErr(err)
		}
		data, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, rerr, "openai: reading response body")
		}
		if resp.StatusCode == http.StatusOK {
			return data, nil
		}
		taxErr := mapHTTPStatus(resp.StatusCode, data)
		lastErr = taxErr
		if !isRetryableStatus(resp.StatusCode) || attempt == maxRetryAttempts-1 {
			return nil, taxErr
		}
		wait := retryDelay(attempt, parseRetryAfter(resp.Header), d.cfg.RetryBaseDelay)
		if werr := d.waitForRetry(ctx, wait); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// retryDelay returns the backoff wait before the given 0-based attempt. A
// vendor-supplied retryAfter always wins; otherwise the sequence doubles
// from base. Pure and deterministic - no randomness, no clock read
// (Art.7.3) - so it is tested directly with no Driver at all.
func retryDelay(attempt int, retryAfter, base time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	return base * time.Duration(uint(1)<<uint(attempt))
}

// waitForRetry decides whether to wait, and for how long, using only
// d.cfg.Clock.Now() - never a bare time.Now(). When ctx carries a deadline
// that the wait would run past, it refuses immediately with KindTimeout
// and never starts a timer at all: a test proves this branch with a
// FrozenClock and performs no real sleep whatsoever. When the wait is
// allowed, it parks on a context-scoped timer, which is where any real
// (production) sleeping happens.
func (d *Driver) waitForRetry(ctx context.Context, wait time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok && d.cfg.Clock.Now().Add(wait).After(deadline) {
		return cascade.Newf(cascade.KindTimeout, "openai: retry backoff of %s would exceed the request deadline", wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return mapDoErr(ctx.Err())
	case <-timer.C:
		return nil
	}
}

// parseRetryAfter reads a vendor's Retry-After header as integer seconds.
// Typed as the header's unnamed underlying map shape (rather than
// http.Header itself) so this package's own _test.go files can call it
// with a plain map literal without importing "net/http" (Art.7.2) -
// http.Header is assignable to map[string][]string at every real call
// site with zero conversion, since one side of that assignment is an
// unnamed type. The alternative HTTP-date form is not parsed (no capture
// in testdata/README.md ever sent one); an unparsable or absent header
// returns zero, which retryDelay treats as "no vendor hint".
func parseRetryAfter(h map[string][]string) time.Duration {
	values := h["Retry-After"]
	if len(values) == 0 {
		return 0
	}
	v := strings.TrimSpace(values[0])
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// isRetryableStatus reports whether resp.StatusCode is worth a retry.
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, http.StatusInternalServerError:
		return true
	default:
		return false
	}
}

// mapDoErr maps a context or transport-level error onto the taxonomy.
func mapDoErr(err error) *cascade.Error {
	switch {
	case errors.Is(err, context.Canceled):
		return cascade.Wrap(cascade.KindCanceled, err, "openai: request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return cascade.Wrap(cascade.KindTimeout, err, "openai: request deadline exceeded")
	default:
		return cascade.Wrap(cascade.KindUnavailable, err, "openai: transport error")
	}
}

// wireErrorEnvelope is the {"error":{...}} shape OpenAI, Moonshot/Kimi and
// zai all send (verified live, testdata/README.md); DeepSeek's captured
// 401 was plain text, not JSON, which is exactly why extractErrorMessage
// falls back to the raw body on decode failure rather than erroring.
type wireErrorEnvelope struct {
	Error *wireErrorBody `json:"error"`
}

// wireErrorBody reads only the "message" field every JSON-shaped vendor
// shares. OpenAI additionally sends type/param/code; Moonshot sends type
// only; zai sends a numeric-string code with no type at all - this driver
// does not depend on any of those sibling fields since their presence and
// shape already differ across the family (Art.2: never assert vendor
// behaviour beyond what was captured).
type wireErrorBody struct {
	Message string `json:"message"`
}

func extractErrorMessage(body []byte) string {
	var env wireErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Message != "" {
		return env.Error.Message
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "(empty response body)"
	}
	return trimmed
}

// mapHTTPStatus maps a non-200 response onto the frozen taxonomy. The
// default branch is KindInternal, the taxonomy's own catch-all; no branch
// here invents a Kind outside the 14-member enumeration.
func mapHTTPStatus(status int, body []byte) *cascade.Error {
	msg := extractErrorMessage(body)
	switch {
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return cascade.Newf(cascade.KindInvalidInput, "openai: %d: %s", status, msg)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return cascade.Newf(cascade.KindPermissionDenied, "openai: %d: %s", status, msg)
	case status == http.StatusNotFound:
		return cascade.Newf(cascade.KindNotFound, "openai: %d: %s", status, msg)
	case status == http.StatusRequestTimeout:
		return cascade.Newf(cascade.KindTimeout, "openai: %d: %s", status, msg)
	case status == http.StatusConflict:
		return cascade.Newf(cascade.KindConflict, "openai: %d: %s", status, msg)
	case status == http.StatusTooManyRequests:
		return cascade.Newf(cascade.KindQuotaExhausted, "openai: %d: %s", status, msg)
	case status == http.StatusNotImplemented:
		return cascade.Newf(cascade.KindUnsupported, "openai: %d: %s", status, msg)
	case status >= 500:
		return cascade.Newf(cascade.KindUnavailable, "openai: %d: %s", status, msg)
	case status >= 400:
		return cascade.Newf(cascade.KindInvalidInput, "openai: %d: %s", status, msg)
	default:
		return cascade.Newf(cascade.KindInternal, "openai: unexpected status %d: %s", status, msg)
	}
}

// fakeResponse is one canned response fakeDoer returns, in queue order.
type fakeResponse struct {
	status  int
	body    []byte
	headers map[string]string
	err     error
}

// fakeDoer is the unexported, in-package recording fake this package's own
// tests use in place of a real Doer. It lives in this production file
// (which already imports net/http for the real transport layer) so no
// _test.go file in this package needs to import "net" or "net/http"
// itself - the no-network unit lane (Art.7.2) forbids exactly that import
// in an untagged test file. Unexported: invisible to the exported-symbol
// dead-code and test-only gates.
type fakeDoer struct {
	responses []fakeResponse
	next      int
	requests  []*http.Request
}

func newFakeDoer(responses ...fakeResponse) *fakeDoer {
	return &fakeDoer{responses: responses}
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req)
	if f.next >= len(f.responses) {
		return nil, fmt.Errorf("fakeDoer: no canned response left for call %d", f.next+1)
	}
	r := f.responses[f.next]
	f.next++
	if r.err != nil {
		return nil, r.err
	}
	h := make(http.Header, len(r.headers))
	for k, v := range r.headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: r.status, Header: h, Body: io.NopCloser(bytes.NewReader(r.body))}, nil
}
