// Purpose: the OAuth broker's three production seams - the loopback
//
//	callback listener, the token-endpoint HTTP client, and the platform
//	browser opener - plus NewOAuthBroker, which wires them together.
//
// Inputs: an OS-assigned ephemeral loopback port, and the provider's HTTPS
//
//	endpoints. Nothing here decides protocol: every rule about what a
//	callback may contain and what a token response must look like lives in
//	oauth_pkce.go, which imports no network package at all.
//
// Outputs: callbackListener / tokenExchanger / browserOpener values.
// Constraints: this is the ONLY file in the OAuth flow that imports "net"
//
//	and "net/http", which is what confines the socket to the integration
//	lane. The listener binds 127.0.0.1:0 - never a fixed port, which
//	another process could squat to steal the authorization code - and
//	closing it releases the port, so a cancelled authorization leaves
//	nothing bound. The browser opener runs a fixed argv per GOOS with the
//	URL as a single non-shell argument; there is no shell on this path, so
//	a hostile authorization URL cannot become a command.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets OAuth transport seams).

package secrets

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// maxTokenResponseBytes bounds what the token endpoint may return. A real
// token response is well under a kilobyte; the cap stops a hostile or
// broken endpoint from streaming until this process dies.
const maxTokenResponseBytes = 1 << 20

// exchangeTimeout bounds one token-endpoint round trip.
const exchangeTimeout = 30 * time.Second

// defaultEntropy is the production entropy source.
func defaultEntropy() io.Reader { return rand.Reader }

// defaultLookupEnv is the production environment reader.
func defaultLookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

// NewOAuthBroker builds the production broker: a real loopback listener, a
// real HTTPS token client, and the platform browser opener.
func NewOAuthBroker(cfg provider.ProviderOAuthConfig, deps OAuthDeps) (*OAuthBroker, error) {
	return newOAuthBroker(cfg, deps, newHTTPExchanger(), listenLoopback, openSystemBrowser)
}

// newHTTPExchanger builds the production token client. Split from
// NewOAuthBroker so a test can drive the REAL exchanger without importing
// net/http itself - the same discipline internal/client's transport tests
// use to cover their production dialer in the default unit lane.
func newHTTPExchanger() tokenExchanger {
	return &httpExchanger{client: &http.Client{Timeout: exchangeTimeout}}
}

// httpExchanger is the production tokenExchanger.
type httpExchanger struct {
	client *http.Client
}

// Exchange posts form to endpoint and returns the body and status. The
// response body is read under a cap and returned as bytes the caller owns
// and zeroes; nothing here logs, and the request's form values - which
// include the code verifier and the refresh token - are never rendered.
func (e *httpExchanger) Exchange(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, cascade.Wrap(cascade.KindInvalidInput, err, "secrets: could not build the OAuth token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, cascade.Wrap(cascade.KindUnavailable, err, "secrets: the OAuth endpoint could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, cascade.Wrap(cascade.KindUnavailable, err, "secrets: the OAuth response body could not be read")
	}
	return body, resp.StatusCode, nil
}

// loopbackListener is the production callbackListener: one bound ephemeral
// port and one buffered slot for the redirect's query string.
type loopbackListener struct {
	listener net.Listener
	server   *http.Server
	queries  chan string
	closed   chan struct{}
}

// listenLoopback binds 127.0.0.1:0 and serves the redirect endpoint. The
// port is OS-assigned: a fixed port is squattable, and a squatted callback
// port receives the authorization code.
func listenLoopback(ctx context.Context) (callbackListener, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", provider.LoopbackHost+":0")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: could not bind a loopback port for the OAuth callback")
	}
	l := &loopbackListener{listener: listener, queries: make(chan string, 1), closed: make(chan struct{})}
	l.server = &http.Server{Handler: http.HandlerFunc(l.handle), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = l.server.Serve(listener) }()
	return l, nil
}

// handle records the first redirect's query string and answers the browser
// with fixed prose. The response body never echoes the query: the browser
// tab, its history, and any screenshot of it must not carry the code.
func (l *loopbackListener) handle(w http.ResponseWriter, r *http.Request) {
	select {
	case l.queries <- r.URL.RawQuery:
	default:
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "cascade received the authorization response. You can close this tab.\n")
}

// Port reports the OS-assigned port.
func (l *loopbackListener) Port() string {
	_, port, err := net.SplitHostPort(l.listener.Addr().String())
	if err != nil {
		return ""
	}
	return port
}

// Wait blocks for the redirect, ctx's deadline, or Close.
func (l *loopbackListener) Wait(ctx context.Context) (string, error) {
	select {
	case q := <-l.queries:
		return q, nil
	case <-l.closed:
		return "", cascade.New(cascade.KindCanceled, "secrets: the OAuth callback listener was closed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close releases the port. Idempotent: Start closes it on every exit path,
// including the ones that already closed it.
//
// The raw listener is closed FIRST and synchronously, before Shutdown. Relying
// on Shutdown alone leaves the release asynchronous - Serve closes the
// listener on its own goroutine, so Close could return while the port was
// still bound, which is exactly what a "the deadline must leave no port
// bound" assertion is testing. Found by TestOAuthLoopbackListenerReleasesItsPort
// under -race. Shutdown still runs afterwards, to drain a handler that is
// mid-response.
func (l *loopbackListener) Close() error {
	select {
	case <-l.closed:
		return nil
	default:
		close(l.closed)
	}
	closeErr := l.listener.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = l.server.Shutdown(shutdownCtx)
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return cascade.Wrap(cascade.KindUnavailable, closeErr, "secrets: the OAuth callback port could not be released")
	}
	return nil
}

// browserCommand returns the argv that opens rawURL on this GOOS, or ok
// false where cascade knows no opener. runtime.GOOS rather than build tags:
// the table is three lines and a build-tagged file per platform would hide
// the fallback branch from every platform's own test run.
func browserCommand(rawURL string) (name string, args []string, ok bool) {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/bin/open", []string{rawURL}, true
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}, true
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{rawURL}, true
	default:
		return "", nil, false
	}
}

// openSystemBrowser is the production browserOpener. The URL is passed as a
// single argv element to a named binary; no shell is involved, so nothing
// in the URL can be interpreted as a command. A failure here is not fatal -
// Start falls back to printing the URL.
func openSystemBrowser(ctx context.Context, rawURL string) error {
	name, args, ok := browserCommand(rawURL)
	if !ok {
		return cascade.Newf(cascade.KindUnsupported, "secrets: no known browser opener for GOOS %q", runtime.GOOS)
	}
	if err := exec.CommandContext(ctx, name, args...).Start(); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "secrets: could not launch %s to open the authorization URL", name)
	}
	return nil
}
