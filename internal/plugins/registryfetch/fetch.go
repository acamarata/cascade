// Package registryfetch is the ONLY package in the tree that performs the
// daemon→registry HTTPS GET (R-21.265): the network half of the plugin
// registry client, whose types, minisign-shaped verification, and local
// cache live in pkg/plugin so that package can stay free of "net" and
// "net/http" (06-FORGE-SPEC §2 — pkg/ never imports internal/, and the
// inverse holds for network imports too: the SDK package stays net-free).
//
// The actual net/http call is isolated behind the package-local Doer
// interface (HTTPRequest/HTTPResponse, both plain data — no net/http
// type), the same pattern internal/providers/intake's transport_test.go
// already documents ("fakeDoer is a recording, in-memory Doer (no
// net/http import; Art.7.2)"): it lets fetch_test.go exercise every
// branch of get() with an in-memory fakeDoer while importing neither
// "net" nor "net/http" itself, satisfying internal/build's
// no-network-unit-lane gate (TestNoNetworkUnitTest_RealTreeGreen), which
// scans _test.go files for those imports regardless of whether a real
// socket opens. The production HTTP call lives in doer.go.
package registryfetch

import (
	"bytes"
	"context"
	neturl "net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress" //nolint:depguard // Registry fetching is an internal egress boundary and must use the shared engine.
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// DefaultTimeout is used when HTTPFetcher.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// maxRedirects matches net/http's default redirect limit.
const maxRedirects = 10

// maxResponseBytes caps every GET's response body so a hostile or broken
// registry cannot stream unbounded bytes into memory.
const maxResponseBytes = 8 << 20 // 8 MiB

// HTTPRequest is the plain-data request shape Doer.Do takes. It carries
// no net/http type, so packages that only need to construct or inspect
// one (including this package's own tests) never import "net/http".
type HTTPRequest struct {
	// URL is the absolute URL to GET.
	URL string
	// CheckRedirect admits each redirect target before it is requested;
	// hops counts the requests already made. A Doer that follows redirects
	// must call it and stop on an error.
	CheckRedirect func(url string, hops int) error
}

// HTTPResponse is the plain-data response shape Doer.Do returns.
type HTTPResponse struct {
	// StatusCode is the response's HTTP status code.
	StatusCode int
	// Body is the response body, already read and capped at
	// maxResponseBytes by the Doer implementation.
	Body []byte
}

// Doer performs one HTTP GET. HTTPFetcher's production behavior is
// implemented entirely in terms of Doer, so the real net/http.Client
// usage lives in exactly one place (realDoer, below).
type Doer interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// HTTPFetcher implements plugin.RegistryFetcher over HTTPS.
type HTTPFetcher struct {
	// BaseURL is the registry root, e.g. "https://registry.example.com/".
	// FetchIndex requests BaseURL+"index.json".
	BaseURL string
	// Timeout bounds one GET, including connection setup. Zero uses
	// DefaultTimeout.
	Timeout time.Duration
	// Doer performs the actual GET. A nil Doer uses the production
	// realDoer, whose client admits every redirect target.
	Doer Doer
	// Engine and Capability are required, including for the default transport.
	Engine     *egress.Engine
	Capability egress.Capability
}

var _ plugin.RegistryFetcher = HTTPFetcher{}

// FetchIndex implements plugin.RegistryFetcher.
func (f HTTPFetcher) FetchIndex(ctx context.Context) ([]byte, error) {
	return f.get(ctx, f.BaseURL+"index.json")
}

// FetchArtifact implements plugin.RegistryFetcher.
func (f HTTPFetcher) FetchArtifact(ctx context.Context, entry plugin.RegistryVersionEntry) ([]byte, error) {
	if err := f.configured(); err != nil {
		return nil, err
	}
	if entry.DownloadURL == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "registryfetch: entry has no download url")
	}
	return f.get(ctx, entry.DownloadURL)
}

func (f HTTPFetcher) get(ctx context.Context, url string) ([]byte, error) {
	if err := f.configured(); err != nil {
		return nil, err
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Admit the outbound target before the transport can perform any I/O.
	if err := f.admitURL(reqCtx, url); err != nil {
		return nil, err
	}
	doer := f.Doer
	if doer == nil {
		doer = realDoer{}
	}
	// A refused redirect is reported by its own sanitized error: the
	// transport's error would quote the refused Location, secret included.
	var refused error
	checkRedirect := func(next string, hops int) error {
		if hops >= maxRedirects {
			refused = cascade.New(cascade.KindUnavailable, "registryfetch: too many redirects")
		} else {
			refused = f.admitURL(reqCtx, next)
		}
		return refused
	}
	resp, err := doer.Do(reqCtx, HTTPRequest{URL: url, CheckRedirect: checkRedirect})
	if refused != nil {
		return nil, refused
	}
	if err != nil {
		if strings.Contains(err.Error(), "Location header") {
			// net/http quotes a Location it cannot parse; that target was
			// never admitted and may carry a secret, so drop the text.
			return nil, cascade.Newf(cascade.KindUnavailable, "registryfetch: GET %s: the redirect location does not parse", url)
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "registryfetch: GET %s", url)
	}
	if resp.StatusCode != 200 {
		return nil, cascade.Wrapf(cascade.KindUnavailable, plugin.ErrRegistryHTTP, "GET %s: status %d", url, resp.StatusCode)
	}
	// Preserve response bytes for downstream parsing and signature verification.
	return resp.Body, nil
}

// admitURL refuses a target before any I/O. Admission is structural first:
// a registry URL needs nothing but a scheme, an ASCII host and a plain path,
// so every other component, every escape and every path a server could
// rewrite is refused outright instead of being searched for secrets
// (admitShape). The egress engine then checks the whole text (capability,
// class, detector shapes, its exact pass), and a strict vault match covers
// host and path down to minStoredValueBytes as defence in depth. Errors
// never echo the URL, because it may hold the secret being refused.
func (f HTTPFetcher) admitURL(ctx context.Context, raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil {
		return cascade.New(cascade.KindInvalidInput, "registryfetch: the target url does not parse")
	}
	if err := admitShape(u, raw); err != nil {
		return err
	}
	if err := f.admitText(ctx, raw); err != nil {
		return err
	}
	return f.admitStoredValues(ctx, u)
}

// minStoredValueBytes is the shortest vault value the strict URL match
// refuses. It is lower than the engine's substitution floor because a URL
// is refused, not rewritten, so a short false positive costs one fetch.
const minStoredValueBytes = 4

// pathBytes is the only alphabet an admitted path may use: unreserved
// characters and '/'. No '%' means no escape layer to decode.
const pathBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._~/-"

// admitShape applies the structural rule: https (http only to a loopback
// test server), an ASCII host, no userinfo, query or fragment, and a path
// of pathBytes with no empty, "." or ".." segment, since servers merge
// slashes and remove dot segments. A final empty segment (trailing '/') is
// allowed: it carries nothing.
func admitShape(u *neturl.URL, raw string) error {
	deny := func(why string) error {
		return cascade.New(cascade.KindPolicyDenied, "registryfetch: refusing a url "+why)
	}
	host := u.Hostname()
	if u.Scheme != "https" && (u.Scheme != "http" || (host != "127.0.0.1" && host != "::1")) {
		return deny("that is not https")
	}
	if u.Opaque != "" || host == "" {
		return deny("without a host")
	}
	for i := 0; i < len(u.Host); i++ {
		if u.Host[i] >= 0x80 {
			return deny("whose host is not ascii")
		}
	}
	// net/http strips an IPv6 zone from the Host header it sends, so the
	// wire authority would differ from the one checked here.
	if strings.Contains(u.Host, "%") {
		return deny("whose host carries a zone")
	}
	if u.User != nil {
		return cascade.New(cascade.KindPolicyDenied, "registryfetch: refusing a url that carries credentials")
	}
	// The raw text decides: Parse drops an empty query or fragment marker.
	if strings.Contains(raw, "?") {
		return deny("that carries a query")
	}
	if strings.Contains(raw, "#") {
		return deny("that carries a fragment")
	}
	// The escaped form is what the request line carries; it equals u.Path
	// exactly when it holds no escape.
	return admitPath(u.EscapedPath(), deny)
}

// admitPath checks the path alphabet and its segments.
func admitPath(path string, deny func(string) error) error {
	for i := 0; i < len(path); i++ {
		if !strings.ContainsRune(pathBytes, rune(path[i])) {
			return deny("whose path is not plain")
		}
	}
	if path == "" {
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, seg := range segments {
		if (seg == "" && i < len(segments)-1) || seg == "." || seg == ".." {
			return deny("whose path a server would rewrite")
		}
	}
	return nil
}

// admitStoredValues refuses a host or path holding a stored vault value of
// at least minStoredValueBytes, with no reference-tag exemption. The host
// compares without case, as DNS does.
func (f HTTPFetcher) admitStoredValues(ctx context.Context, u *neturl.URL) error {
	for _, part := range []struct {
		text string
		fold bool
	}{{u.Host, true}, {u.Path, false}} {
		found, err := f.Engine.StoredValueIn(ctx, f.Capability, part.text, minStoredValueBytes, part.fold)
		if err != nil {
			return err
		}
		if found {
			return cascade.New(cascade.KindPolicyDenied, "registryfetch: refusing a url that carries secret material")
		}
	}
	return nil
}

// admitText refuses text the engine blocks or rewrites.
func (f HTTPFetcher) admitText(ctx context.Context, text string) error {
	out, err := f.Engine.Intercept(ctx, f.Capability, egress.TierPublic, []byte(text))
	if err != nil {
		return err
	}
	if !bytes.Equal(out, []byte(text)) {
		return cascade.New(cascade.KindPolicyDenied, "registryfetch: refusing a url that carries secret material")
	}
	return nil
}

// configured refuses incomplete wiring before the transport can open a socket.
func (f HTTPFetcher) configured() error {
	if f.Engine == nil || f.Engine.Registry() == nil || f.Capability.Class() != egress.EgressClassRegistryFetch {
		return cascade.New(cascade.KindUnavailable, "registryfetch: a configured engine and registry-fetch capability are required")
	}
	return nil
}
