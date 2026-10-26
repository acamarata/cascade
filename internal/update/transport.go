package update

// Purpose: the one net/http importer in internal/update: the release
//   download transport behind Fetcher and Locator.
// Inputs: request URLs; an injected Interceptor; the Source base URL whose
//   host joins the pinned set.
// Outputs: response bodies (status 200) or a redirect target.
// Constraints: every request and every redirect is admitted BEFORE any
//   connection: https only (http only to the loopback base URL host), host
//   pinned to github.com, objects.githubusercontent.com,
//   release-assets.githubusercontent.com or exactly the base URL host, no
//   credentials or fragment, then exactly one InterceptClass call with
//   class release-download, tier public and the URL as content. An error or
//   a nil Interceptor refuses every request. A rewrite refuses every URL
//   this package built (base URL, tag, file name) and every redirect target
//   except one on objects.githubusercontent.com or
//   release-assets.githubusercontent.com: GitHub's signed asset redirect
//   carries jwt and high-entropy query fields that the detector's shape
//   pass rewrites (a real redirect was probed and is rewritten), and those
//   bytes were issued by the pinned server, so there the rewrite is
//   tolerated and the target is requested as issued. At most 5 redirects.
//   One requestDeadline bounds each request, its redirects and its body. No proxy from the
//   environment: a proxy would be an unpinned peer. Refusals never echo the
//   URL, which may carry a signed query.
// SPORT: internal/update HTTPFetcher/ADDED.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxRedirects is the redirect budget per request.
const maxRedirects = 5

// signedAssetHosts serve GitHub's signed release-asset redirects, whose
// query (jwt, sig and other high-entropy fields) the detector's shape pass
// rewrites. Only a redirect target on one of these may be rewritten and
// still requested; a rewritten redirect to any other host (github.com, the
// base URL host) refuses.
var signedAssetHosts = map[string]bool{
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// requestDeadline bounds one request including its redirects and body (a
// pinned peer that stalls mid-body cannot hold an update forever). A
// variable only so a test can shorten it.
var requestDeadline = 10 * time.Minute

// pinnedHosts are the release hosts the default source redirects through.
var pinnedHosts = map[string]bool{
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// releaseTransport is shared so connections are reused across one update.
var releaseTransport = &http.Transport{
	Proxy:                 nil,
	ForceAttemptHTTP2:     true,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	IdleConnTimeout:       30 * time.Second,
	MaxIdleConns:          4,
}

// Interceptor is the egress transit seam; the CLI passes ProcessInterceptor.
type Interceptor interface {
	InterceptClass(ctx context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error)
}

// HTTPFetcher fetches release files over HTTPS through the Interceptor.
type HTTPFetcher struct {
	// Interceptor must be non-nil; nil refuses every request.
	Interceptor Interceptor
	// BaseURL is the Source base; its host:port is the one extra pinned host.
	BaseURL string
}

var (
	_ Fetcher = HTTPFetcher{}
	_ Locator = HTTPFetcher{}
)

func refusedURL(why string) error {
	return cascade.New(cascade.KindPolicyDenied, "update: refusing a release request "+why)
}

// admit applies the shape and host pin, then the egress transit; redirect
// marks a server-issued target, whose rewrite is tolerated.
func (f HTTPFetcher) admit(ctx context.Context, raw string, redirect bool) error {
	u, err := neturl.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return refusedURL("whose URL does not parse as absolute")
	}
	if u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || strings.Contains(u.Host, "%") {
		return refusedURL("that carries credentials, a fragment or a zone")
	}
	if !f.hostAllowed(u) {
		return refusedURL("to unpinned host " + u.Hostname())
	}
	if f.Interceptor == nil {
		return cascade.New(cascade.KindUnavailable, "update: no egress interceptor is configured; refusing the release request")
	}
	out, err := f.Interceptor.InterceptClass(ctx, egress.EgressClassReleaseDownload, egress.TierPublic, []byte(raw))
	if err != nil {
		return err
	}
	if !bytes.Equal(out, []byte(raw)) && !rewriteTolerated(redirect, u) {
		return refusedURL("that the egress firewall rewrote")
	}
	return nil
}

// rewriteTolerated reports whether a detector rewrite of u may be ignored:
// only for a server-issued https redirect to a signed release-asset host.
func rewriteTolerated(redirect bool, u *neturl.URL) bool {
	return redirect && u.Scheme == "https" && signedAssetHosts[strings.ToLower(u.Hostname())]
}

// hostAllowed reports whether u's scheme and host are pinned.
func (f HTTPFetcher) hostAllowed(u *neturl.URL) bool {
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "https" && u.Port() == "" && pinnedHosts[host] {
		return true
	}
	base, err := neturl.Parse(f.BaseURL)
	if f.BaseURL == "" || err != nil || !strings.EqualFold(base.Host, u.Host) {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && base.Scheme == "http" && isLoopbackHost(host)
}

// do performs one admitted GET; follow decides whether redirects are taken.
func (f HTTPFetcher) do(ctx context.Context, raw string, follow bool) (*http.Response, error) {
	if err := f.admit(ctx, raw, false); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, refusedURL("whose URL does not build a request")
	}
	var refused error
	client := &http.Client{Transport: releaseTransport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		switch {
		case !follow:
			return http.ErrUseLastResponse
		case len(via) > maxRedirects:
			refused = refusedURL("after too many redirects")
		default:
			refused = f.admit(ctx, next.URL.String(), true)
		}
		return refused
	}}
	resp, err := client.Do(req)
	if refused != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, refused
	}
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "update: release request to %s failed", u0(raw))
	}
	return resp, nil
}

// u0 is the scheme://host part of raw, for error text.
func u0(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "the release host"
	}
	return u.Scheme + "://" + u.Host
}

// Get returns the body of a 200 response, following admitted redirects.
// The request, its redirects and the whole body share one requestDeadline.
func (f HTTPFetcher) Get(ctx context.Context, raw string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, requestDeadline)
	resp, err := f.do(ctx, raw, true)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		return nil, cascade.Newf(cascade.KindUnavailable, "update: release request to %s answered %d", u0(raw), resp.StatusCode)
	}
	return cancelOnClose{ReadCloser: resp.Body, cancel: cancel}, nil
}

// cancelOnClose ends the request's deadline context when the body closes.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// Locate returns the redirect target of raw without following it, within
// one requestDeadline.
func (f HTTPFetcher) Locate(ctx context.Context, raw string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()
	resp, err := f.do(ctx, raw, false)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	loc, err := resp.Location()
	if resp.StatusCode < 300 || resp.StatusCode > 399 || err != nil {
		return "", cascade.Newf(cascade.KindUnavailable, "update: %s did not redirect to a release (status %d)", u0(raw), resp.StatusCode)
	}
	return loc.String(), nil
}
