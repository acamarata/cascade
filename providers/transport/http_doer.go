// Package transport is the real net/http transport shared by every
// providers/* driver internal/providers/dispatch constructs, and the
// Transport seam that decouples it from *http.Client so a test can supply
// a fake with no net/http import. It lives under providers/ rather than
// internal/providers/dispatch (where it was first built) for two tied
// reasons, both verified against the tree rather than assumed: providers/*
// is already a normative member of the egress ruling's net-import
// allowlist (internal/build/egress_allow.go's EgressNetNormative), so this
// package needs no allowlist change and adds no new ruling exception; and
// providers/** is Art.4's TierPlugins (80% floor - internal/build/
// coveragegate.go's PackageTier), the tier a vendor transport shim
// belongs in, rather than TierCore's 85% floor internal/providers/dispatch
// carries as an internal/** package. Moving it here also leaves
// internal/providers/dispatch with no net/http import at all, so its own
// resolver logic is fully exercised by the default unit lane.
//
// Purpose (this file): the Transport interface, its real implementation
// (NewHTTPTransport/netTransport), and the four per-provider adapters
// (AnthropicDoer/GeminiDoer/OllamaDoer/OpenAIDoer) translating each
// driver's own locally-declared HTTPDoer/Doer interface onto it. Each
// driver package declares its own local Doer interface (providers/** may
// import pkg/** only, never internal/** - 12-QUALITY-CONSTITUTION.md
// Art.10.2, enforced by .golangci.yml's plugins-providers-boundary and
// internal/build/arch_test.go); anthropic, gemini and ollama each get a
// tiny adapter translating their package-local HTTPRequest/HTTPResponse
// shape onto Transport, and openai's own Doer (Do(*http.Request)
// (*http.Response, error)) gets one translating the other direction, so
// internal/providers/dispatch's own Resolver/NewResolver never needs to
// name *http.Client itself - every test in that package, including the
// ones that build all four drivers, stays decoupled from net/http.
//
// Inputs: the outbound request fields a driver's own HTTPRequest carries
// (method, URL, headers, body), or an openai *http.Request.
//
// Outputs: the inbound response fields a driver's own HTTPResponse
// expects (status code, response body), or an openai *http.Response
// (status, header and body all preserved: providers/openai's own retry
// logic reads Retry-After off the response header).
//
// Constraints: no retry, no credential handling and no interpretation of
// the response body here - this is pure transport, identical to what a
// driver would get from *http.Client directly. Errors return unwrapped;
// each driver package maps its own Doer errors to the taxonomy.
//
// SPORT: providers/transport (ADD, DEFECT-conductor-execute-permanently-
// unavailable.md; moved from internal/providers/dispatch per
// FIX-transport-tier-relocation.md; request/response translation shrunk
// to plain-typed helpers per FIX-transport-coverage-shrink.md so the
// package clears its own TierPlugins floor).
package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/acamarata/cascade/providers/anthropic"
	"github.com/acamarata/cascade/providers/gemini"
	"github.com/acamarata/cascade/providers/ollama"
)

// Transport is the seam every driver adapter in this file sends through.
// NewHTTPTransport's return value is the only production implementation;
// tests substitute a fake that never opens a socket. Exported so
// internal/providers/dispatch's composition-root caller can build the
// real one with NewHTTPTransport and pass it into NewResolver without
// that package's own public API ever naming *http.Client.
type Transport interface {
	Send(ctx context.Context, method, url string, headers map[string]string, body []byte) (status int, respHeaders map[string][]string, respBody io.ReadCloser, err error)
}

// netTransport is Transport's real implementation: a thin wrapper over
// *http.Client. This is the one place in this package that touches
// net/http outside newRequest/OpenAIDoer's own req/resp translation -
// every test, including every test that builds all four drivers, is
// decoupled from it through Transport.
type netTransport struct{ client *http.Client }

// NewHTTPTransport wraps client as a Transport. The composition root
// calls this once and passes the result to internal/providers/dispatch's
// NewResolver; client must not be nil.
func NewHTTPTransport(client *http.Client) Transport {
	return netTransport{client: client}
}

func (t netTransport) Send(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, map[string][]string, io.ReadCloser, error) {
	req, err := newRequest(ctx, method, url, headers, body)
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, map[string][]string(resp.Header), resp.Body, nil
}

// newRequest builds the outbound *http.Request. Constructing a
// *http.Request needs the "net/http" import this package's own default-
// lane tests may not carry (see this file's header comment), so this
// function is only exercised by the integration-tagged real-transport
// test; requestBody and headerSlice below are split out precisely so the
// two pieces of real branching/mapping logic here (empty vs non-empty
// body, single- to multi-valued header conversion) are still unit tested
// with no net/http import of their own.
func newRequest(ctx context.Context, method, url string, headers map[string]string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, requestBody(body))
	if err != nil {
		return nil, err
	}
	req.Header = http.Header(headerSlice(headers))
	return req, nil
}

// requestBody returns nil for an empty body (matching
// http.NewRequestWithContext's own no-body convention) or an io.Reader
// over body otherwise. Pure, and needs no net/http import, so it is unit
// tested directly in http_doer_test.go.
func requestBody(body []byte) io.Reader {
	if len(body) == 0 {
		return nil
	}
	return bytes.NewReader(body)
}

// headerSlice converts the single-valued header map every driver's own
// HTTPRequest.Headers carries into net/http's multi-valued header shape
// (a plain map[string][]string rather than the named http.Header type, so
// this function itself needs no net/http reference and no import in its
// test). newRequest above is the only caller that applies the result to a
// real *http.Request; the mapping itself is pure and unit tested directly.
func headerSlice(headers map[string]string) map[string][]string {
	out := make(map[string][]string, len(headers))
	for k, v := range headers {
		out[k] = []string{v}
	}
	return out
}

// AnthropicDoer adapts Transport to anthropic.HTTPDoer. Exported so
// internal/providers/dispatch's build.go can construct one from the
// Transport it holds.
type AnthropicDoer struct{ Transport Transport }

// Do sends req through the wrapped Transport and translates the result
// back into anthropic.HTTPResponse.
func (d AnthropicDoer) Do(ctx context.Context, req anthropic.HTTPRequest) (anthropic.HTTPResponse, error) {
	status, _, body, err := d.Transport.Send(ctx, req.Method, req.URL, req.Headers, req.Body)
	if err != nil {
		return anthropic.HTTPResponse{}, err
	}
	return anthropic.HTTPResponse{Status: status, Body: body}, nil
}

// GeminiDoer adapts Transport to gemini.HTTPDoer.
type GeminiDoer struct{ Transport Transport }

// Do sends req through the wrapped Transport and translates the result
// back into gemini.HTTPResponse.
func (d GeminiDoer) Do(ctx context.Context, req gemini.HTTPRequest) (gemini.HTTPResponse, error) {
	status, _, body, err := d.Transport.Send(ctx, req.Method, req.URL, req.Headers, req.Body)
	if err != nil {
		return gemini.HTTPResponse{}, err
	}
	return gemini.HTTPResponse{Status: status, Body: body}, nil
}

// OllamaDoer adapts Transport to ollama.HTTPDoer.
type OllamaDoer struct{ Transport Transport }

// Do sends req through the wrapped Transport and translates the result
// back into ollama.HTTPResponse.
func (d OllamaDoer) Do(ctx context.Context, req ollama.HTTPRequest) (ollama.HTTPResponse, error) {
	status, _, body, err := d.Transport.Send(ctx, req.Method, req.URL, req.Headers, req.Body)
	if err != nil {
		return ollama.HTTPResponse{}, err
	}
	return ollama.HTTPResponse{Status: status, Body: body}, nil
}

// OpenAIDoer adapts Transport to openai.Doer (Do(*http.Request)
// (*http.Response, error)): the one direction the translation runs the
// other way, since openai's own Doer already speaks net/http's types
// directly. Building the *http.Request/*http.Response values needs
// net/http, so - like newRequest above - this type's Do method is only
// exercised by the integration-tagged test; flattenHeader is split out
// pure so the lossy multi-value-header behavior is still unit tested.
type OpenAIDoer struct{ Transport Transport }

// Do builds the outbound *http.Request's fields into a Transport.Send
// call and translates the result back into a *http.Response. Reading the
// body and flattening the header are both pulled out into readBody/
// flattenHeader below so the only statements left here that need
// net/http's own types are the ones that actually name *http.Request/
// *http.Response.
func (d OpenAIDoer) Do(req *http.Request) (*http.Response, error) {
	body, err := readBody(req.Body)
	if err != nil {
		return nil, err
	}
	status, respHeaders, respBody, err := d.Transport.Send(req.Context(), req.Method, req.URL.String(), flattenHeader(req.Header), body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header(respHeaders), Body: respBody}, nil
}

// readBody drains rc, or returns (nil, nil) for a nil body (an outbound
// request with no body, matching *http.Request.Body's own nil convention
// when unset). Takes io.ReadCloser rather than *http.Request so it needs
// no net/http import and is unit tested directly in http_doer_test.go;
// OpenAIDoer.Do above is the only caller that supplies a real request's
// Body field.
func readBody(rc io.ReadCloser) ([]byte, error) {
	if rc == nil {
		return nil, nil
	}
	return io.ReadAll(rc)
}

// flattenHeader takes the first value of every multi-value header entry:
// Transport.Send's outbound headers parameter is single-valued, matching
// every other driver's own HTTPRequest.Headers shape. Pure, no net/http
// import, unit tested directly.
func flattenHeader(h map[string][]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
