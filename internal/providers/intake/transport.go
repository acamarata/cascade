// Purpose: the outbound HTTP seam (P1-E10-W3-S20-T1): the shape probe, in
//   the normative anthropic-compat -> openai-compat -> gemini order, model
//   enumeration from that same response, and the live 1-token micro-verify.
//   This package is a NORMATIVE net/net-http importer (internal/build's
//   egress allowlist names internal/providers/intake by this ticket ID),
//   so httpDoer below may use *http.Client directly; every call still
//   transits the H/S-16.T1 firewall's Intercept BEFORE a byte reaches it.
// Inputs: a Doer (production: httpDoer over *http.Client; test: a
//   recording fake that never imports net/http, keeping transport_test.go
//   in the no-network unit lane per Art.7.2) and an *egress.Engine.
// Outputs: (DriverKind, base URL, []string models) on a matched shape, or
//   a multi-endpoint typed refusal; a micro-verify HTTP status/body pair.
// Constraints: NO NETWORK in transport_test.go - only this file (a
//   non-_test.go file) may construct httpDoer. Every outbound body is
//   passed through egress.Engine.Intercept first (R-21.265); a call made
//   with a zero Capability or against a disabled class returns before any
//   byte is read, let alone sent.
// SPORT: provider.intake/ADD (P1-E10-W3-S20-T1).

package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Doer performs one outbound HTTP call in this package's own vocabulary.
// Declared locally (matching every landed driver's own pattern) so a
// recording fake never needs "net"/"net/http" to satisfy it.
type Doer interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// HTTPRequest is one outbound request.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is one inbound response, fully buffered: intake's calls are
// all small (a models list, a 1-token completion), so there is no reason
// to stream and every reason to keep the seam simple.
type HTTPResponse struct {
	Status int
	Body   []byte
}

// httpDoer is the production Doer, over a real *http.Client. It is the
// "production caller wires a small *http.Client adapter" this ticket owns
// per providers/anthropic's HTTPRequest doc comment.
type httpDoer struct {
	client *http.Client
}

// NewHTTPDoer returns a production Doer with a bounded per-call timeout;
// intake calls are all small, so a generous fixed timeout costs nothing on
// the happy path and bounds the unhappy one. Exported for cmd/cascade's
// composition root; never called from a _test.go file in this package
// (that would import net/http into the no-network unit lane).
func NewHTTPDoer(timeout time.Duration) Doer {
	return httpDoer{client: &http.Client{Timeout: timeout}}
}

func (d httpDoer) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	hreq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return HTTPResponse{}, cascade.Wrap(cascade.KindInvalidInput, err, "intake: building the outbound request")
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	resp, err := d.client.Do(hreq)
	if err != nil {
		return HTTPResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "intake: the outbound request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "intake: reading the response body")
	}
	return HTTPResponse{Status: resp.StatusCode, Body: body}, nil
}

// probeAttempt records one shape-probe leg, success or failure, for the
// all-fail error message.
type probeAttempt struct {
	kind     DriverKind
	endpoint string
	status   int
	err      error
}

// probeTarget is one candidate the shape probe tries, in normative order.
type probeTarget struct {
	kind       DriverKind
	method     string
	pathSuffix string
	headers    func(key string) map[string]string
}

// defaultBases gives each driver kind its default API root, used when the
// caller supplies no --base-url override.
var defaultBases = map[DriverKind]string{
	DriverAnthropic:    "https://api.anthropic.com",
	DriverOpenAICompat: "https://api.openai.com",
	DriverGemini:       "https://generativelanguage.googleapis.com",
}

// probeOrder is the 08-INIT-CONFIG-SPEC.md §2 normative sequence: try
// anthropic-compat, then openai-compat, then gemini. Never reordered.
var probeOrder = []probeTarget{
	{DriverAnthropic, http.MethodGet, "/v1/models", func(key string) map[string]string {
		return map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}
	}},
	{DriverOpenAICompat, http.MethodGet, "/v1/models", func(key string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + key}
	}},
	{DriverGemini, http.MethodGet, "/v1beta/models", nil},
}

// acquireIntakeGate runs exactly the two checks R-21.265 requires before
// any byte leaves the process on the provider-intake class: acquire the
// unforgeable Capability, then the R-21.228 sensitivity pass over the
// declared tier. It deliberately stops there and never runs
// Engine.Intercept's content-substitution pass: every outbound body here
// is JSON this package constructs itself (a model id, a fixed "hi" probe
// string), never operator-typed text that might carry an accidental
// secret, and the credential itself travels only in a header this
// function never sees - substitution has nothing to protect here and the
// detector's entropy signal false-positives on an ordinary model id.
func acquireIntakeGate(engine *egress.Engine, tier egress.SensitivityTier) (egress.Capability, error) {
	token, err := engine.Capability(egress.EgressClassProviderIntake)
	if err != nil {
		return egress.Capability{}, errEgressClassRefused(err)
	}
	cfg, ok := engine.Registry().Lookup(egress.EgressClassProviderIntake)
	if !ok {
		return egress.Capability{}, errEgressClassRefused(err)
	}
	if serr := egress.SensitivityPass(egress.EgressClassProviderIntake, cfg, tier); serr != nil {
		return egress.Capability{}, errEgressClassRefused(serr)
	}
	return token, nil
}

// shapeProbe tries each candidate in probeOrder over doer, gated by
// engine's provider-intake class. baseOverride, when non-empty, replaces
// every candidate's default base (an operator-supplied --base-url names
// one specific server, so the probe tries only that driver's shape
// against it - never a mix of bases across kinds).
func shapeProbe(ctx context.Context, doer Doer, engine *egress.Engine, key, baseOverride string) (DriverKind, string, []byte, error) {
	if _, err := acquireIntakeGate(engine, egress.TierInternal); err != nil {
		return "", "", nil, err
	}
	var attempts []probeAttempt
	for _, target := range probeOrder {
		base := baseOverride
		if base == "" {
			base = defaultBases[target.kind]
		}
		endpoint := base + target.pathSuffix
		url := endpoint
		if target.kind == DriverGemini {
			// The credential travels as a query parameter on this one
			// vendor's endpoint - real for the wire call, but NEVER
			// recorded in probeAttempt.endpoint: that value reaches the
			// operator through errShapeProbeFailed's message, and "an
			// error that echoes the input" is exactly the credential leak
			// AGENT-BRIEF calls out by name.
			url += "?key=" + key
			endpoint += "?key=REDACTED"
		}
		headers := map[string]string{}
		if target.headers != nil {
			headers = target.headers(key)
		}
		resp, err := doer.Do(ctx, HTTPRequest{Method: target.method, URL: url, Headers: headers})
		if err != nil {
			attempts = append(attempts, probeAttempt{kind: target.kind, endpoint: endpoint, err: err})
			continue
		}
		if resp.Status == http.StatusOK {
			return target.kind, base, resp.Body, nil
		}
		attempts = append(attempts, probeAttempt{kind: target.kind, endpoint: endpoint, status: resp.Status})
	}
	return "", "", nil, errShapeProbeFailed(attempts)
}

// modelsFromProbe decodes body against kind's known models-list shape and
// returns the model IDs, in the order the vendor listed them. An unknown
// shape (a 200 this build cannot decode) is the fail-closed refusal
// errUnknownEndpointShape - never a partially-populated model list.
func modelsFromProbe(kind DriverKind, body []byte) ([]string, error) {
	switch kind {
	case DriverAnthropic:
		var wire struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, errUnknownEndpointShape(kind, http.StatusOK)
		}
		out := make([]string, 0, len(wire.Data))
		for _, m := range wire.Data {
			out = append(out, m.ID)
		}
		return out, nil
	case DriverOpenAICompat:
		var wire struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, errUnknownEndpointShape(kind, http.StatusOK)
		}
		out := make([]string, 0, len(wire.Data))
		for _, m := range wire.Data {
			out = append(out, m.ID)
		}
		return out, nil
	case DriverGemini:
		var wire struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, errUnknownEndpointShape(kind, http.StatusOK)
		}
		out := make([]string, 0, len(wire.Models))
		for _, m := range wire.Models {
			out = append(out, m.Name)
		}
		return out, nil
	case DriverOllama, DriverLocalLLM:
		// Local drivers are never shape-probed (there is no vendor
		// credential to try): they are selected only by an explicit
		// directive, and their own driver package owns model enumeration.
		return nil, errUnknownEndpointShape(kind, http.StatusOK)
	default:
		return nil, errUnknownEndpointShape(kind, http.StatusOK)
	}
}

// microVerifyBody builds the 1-token completion request body for model
// under kind.
func microVerifyBody(kind DriverKind, model string) []byte {
	// Anthropic and every openai-compat-shaped driver (openai-compat,
	// ollama, localllm) share one request shape; only gemini differs.
	// One case list per shape, exhaustive over all 5 DriverKind members,
	// avoids repeating the JSON literal per member.
	payload := map[string]any{
		"model": model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	switch kind {
	case DriverGemini:
		payload = map[string]any{
			"contents":         []map[string]any{{"parts": []map[string]string{{"text": "hi"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 1},
		}
	case DriverAnthropic, DriverOpenAICompat, DriverOllama, DriverLocalLLM:
		// payload already set to the shared shape above.
	}
	b, _ := json.Marshal(payload)
	return b
}

// microVerifyRequest builds the full HTTPRequest for the live micro-verify
// call, per driver kind's chat-completion endpoint.
func microVerifyRequest(kind DriverKind, base, key, model string) HTTPRequest {
	body := microVerifyBody(kind, model)
	switch kind {
	case DriverAnthropic:
		return HTTPRequest{Method: http.MethodPost, URL: base + "/v1/messages", Body: body,
			Headers: map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01", "content-type": "application/json"}}
	case DriverGemini:
		return HTTPRequest{Method: http.MethodPost, URL: base + "/v1beta/models/" + model + ":generateContent?key=" + key, Body: body,
			Headers: map[string]string{"content-type": "application/json"}}
	case DriverOpenAICompat, DriverOllama, DriverLocalLLM:
	}
	return HTTPRequest{Method: http.MethodPost, URL: base + "/v1/chat/completions", Body: body,
		Headers: map[string]string{"Authorization": "Bearer " + key, "content-type": "application/json"}}
}
