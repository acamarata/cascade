package auth

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the loopback half of the PKCE flow — bind an
//
//	ephemeral port on 127.0.0.1, wait for the authorization server to
//	redirect the browser back, and hand the verified code to the caller.
//
// Inputs: the PKCE values that started the flow, and the redirect itself.
// Outputs: the authorization code, or a typed refusal.
// Constraints: the listener is bound to 127.0.0.1 ONLY — never 0.0.0.0 —
//
//	so the callback cannot be delivered by anything off this machine. The
//	wait is bounded by CallbackTimeout: an abandoned consent screen must
//	not hold a socket open for the life of the process. State is checked by
//	VerifyCallback before the code is accepted, which is what makes a
//	forged callback fail instead of authorizing someone else's flow.
//
//	net/http lives here and in exchange.go only. Art.7.2 forbids an
//	untagged _test.go from importing net or net/http, so this file's socket
//	behavior is exercised by loopback_integration_test.go behind the
//	`integration` build tag, and the decision logic it delegates to
//	(VerifyCallback) is unit-tested directly.
//
// SPORT: plugins/github/auth:loopback (ADD) — P1-E25-W5-S51-T1.

// callbackPath is the path the authorization server redirects to. It is
// fixed rather than random: the listener is already private to this
// process by virtue of its ephemeral loopback port.
const callbackPath = "/callback"

// Loopback is a bound, listening callback endpoint.
type Loopback struct {
	listener net.Listener
	server   *http.Server
	incoming chan url.Values
}

// Listen binds an ephemeral port on 127.0.0.1 and begins serving the
// callback endpoint. The caller must Close it.
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
	mux.HandleFunc(callbackPath, lb.handle)
	lb.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = lb.server.Serve(listener) }()

	return lb, nil
}

// handle records one redirect and tells the human they can close the tab.
//
// It deliberately reports nothing about whether the flow succeeded: the
// state check happens in Wait, and a browser page is not a trustworthy
// place to report an authorization failure to a machine.
func (l *Loopback) handle(w http.ResponseWriter, r *http.Request) {
	select {
	case l.incoming <- r.URL.Query():
	default: // a redirect already arrived; the first one wins
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Cascade received the GitHub response. You can close this tab."))
}

// RedirectURI is the address to register with the authorization server.
func (l *Loopback) RedirectURI() string {
	return "http://" + l.listener.Addr().String() + callbackPath
}

// Wait blocks until the callback arrives, the context is cancelled, or
// CallbackTimeout elapses, then verifies it against the PKCE state.
//
// The timeout is part of the contract rather than a convenience: a flow
// nobody completes must end, and end as a typed error the caller can report
// rather than as a process that never answers.
func (l *Loopback) Wait(ctx context.Context, p PKCE) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CallbackTimeout)
	defer cancel()

	select {
	case query := <-l.incoming:
		return VerifyCallback(query, p)
	case <-ctx.Done():
		return "", cascade.Wrapf(cascade.KindTimeout, ctx.Err(),
			"github: no OAuth callback arrived within %s", CallbackTimeout)
	}
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
