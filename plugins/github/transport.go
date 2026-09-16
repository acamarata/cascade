package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/auth"
)

// Purpose (this file): the plugin's two real transports — the loopback
//
//	listener that receives the OAuth redirect, and the POST to the token
//	endpoint.
//
// Inputs: the decisions plugins/github/auth makes; a bound port; a form.
// Outputs: a verified authorization code, and a token response.
// Constraints: this is the ONLY file in the plugin that opens a socket, and
//
//	it lives at the composition root on purpose. The auth package holds the
//	decisions — the state check, the timeout, first-redirect-wins, the
//	exchange rules — and imports no net transport at all, so every one of
//	those is testable in the default unit lane, which Art.7.2 forbids from
//	importing net.
//
//	Splitting it the other way round (decisions next to the socket) is what
//	made those rules reachable only from the integration lane, and the
//	coverage gate reads the default lane only. See
//	journals/RULING-coverage-socket-code.md.
//
// SPORT: plugins/github:transport (ADD) — P1-E25-W5-S51-T1.

// maxTokenBody caps how much of a token response is read. A token response
// is a few hundred bytes; anything approaching this is not one, and reading
// it unbounded would let the far end grow this process.
const maxTokenBody = 1 << 20

// ExchangeTimeout bounds a token-endpoint call.
const ExchangeTimeout = 30 * time.Second

// Loopback is a bound, listening callback endpoint.
type Loopback struct {
	listener net.Listener
	server   *http.Server
	incoming chan url.Values
}

// Listen binds an ephemeral port on 127.0.0.1 and serves the callback
// endpoint. The caller must Close it.
func Listen() (*Loopback, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err,
			"github: binding the OAuth callback listener")
	}

	lb := &Loopback{
		listener: listener,
		// Buffered so the handler never blocks on a caller that has
		// already given up waiting: it records the redirect, answers the
		// browser, and returns.
		incoming: make(chan url.Values, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(auth.CallbackPath, lb.handle)
	lb.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = lb.server.Serve(listener) }()

	return lb, nil
}

// handle records one redirect and tells the human they can close the tab.
func (l *Loopback) handle(w http.ResponseWriter, r *http.Request) {
	auth.RecordCallback(l.incoming, r.URL.Query())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(auth.CallbackPageBody))
}

// RedirectURI is the address to register with the authorization server.
func (l *Loopback) RedirectURI() string {
	return "http://" + l.listener.Addr().String() + auth.CallbackPath
}

// Wait blocks for the callback and verifies it against the PKCE state.
func (l *Loopback) Wait(ctx context.Context, p auth.PKCE) (string, error) {
	return auth.AwaitCallback(ctx, l.incoming, p, auth.CallbackTimeout)
}

// Close stops the listener. It is safe to call more than once.
func (l *Loopback) Close() error {
	if l.server == nil {
		return nil
	}
	if err := l.server.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "github: closing the OAuth callback listener")
	}
	return nil
}

// HTTPPoster is the real token-endpoint transport.
func HTTPPoster(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ExchangeTimeout)
	defer cancel()

	req, err := buildTokenRequest(ctx, endpoint, form)
	if err != nil {
		return 0, nil, err
	}

	resp, err := (&http.Client{Timeout: ExchangeTimeout}).Do(req)
	if err != nil {
		return 0, nil, cascade.Wrap(cascade.KindUnavailable, err, "github: calling the token endpoint")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readTokenBody(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// buildTokenRequest renders the token-endpoint POST.
//
// The Accept header is the non-obvious half: GitHub's token endpoint
// answers with a FORM-ENCODED body by default, not JSON. Without
// "Accept: application/json" the response decodes to a zero TokenResponse —
// no token and no error — which surfaces as a confusing integrity failure
// rather than as the missing header it actually is.
func buildTokenRequest(ctx context.Context, endpoint string, form url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "github: building the token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// readTokenBody reads a capped token response.
func readTokenBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxTokenBody))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "github: reading the token response")
	}
	return body, nil
}
