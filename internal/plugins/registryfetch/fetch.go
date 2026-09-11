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
// socket opens. Only this file (fetch.go, not a _test.go) imports
// net/http, exactly as the egress allowlist expects.
//
// EGRESS WIRING GAP (recorded here, not silently omitted): the ticket
// text for P1-E24-W5-S50-T1 requires every fetched byte to transit
// internal/secrets' Intercept(ctx, EgressClassRegistryFetch, tier,
// content) and requires EgressClassRegistryFetch to be appended to "the
// H/S-16.T1 inventory (internal/secrets/classes.go)". Neither exists in
// this tree: internal/secrets has no classes.go, and grepping the whole
// module for "func Intercept", "type EgressClass", "func RegisterClass",
// and "type Capability" returns zero matches anywhere, including in
// P1-E08-W2-S16-T1's own declared package. That ticket is this one's
// depends_on and is not built. Building the substitution/sensitivity
// firewall here would mean constructing another ticket's owned subsystem
// inside this one, risking an incompatible collision when S-16.T1
// actually lands its own Intercept/EgressClass/RegisterClass surface.
// This package therefore ships its HTTPS fetch WITHOUT the egress-door
// wiring the ticket describes, and that gap is reported as an unmet
// acceptance criterion rather than papered over with a fabricated
// interception call. internal/build/egress_allow.go already lists this
// package's directory in EgressNetNormative with the reason "the plugin
// registry fetch" (pre-existing, no change needed by this ticket).
package registryfetch

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// DefaultTimeout is used when HTTPFetcher.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// maxResponseBytes caps every GET's response body so a hostile or broken
// registry cannot stream unbounded bytes into memory.
const maxResponseBytes = 8 << 20 // 8 MiB

// HTTPRequest is the plain-data request shape Doer.Do takes. It carries
// no net/http type, so packages that only need to construct or inspect
// one (including this package's own tests) never import "net/http".
type HTTPRequest struct {
	// URL is the absolute URL to GET.
	URL string
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
	// realDoer backed by http.DefaultClient.
	Doer Doer
}

var _ plugin.RegistryFetcher = HTTPFetcher{}

// FetchIndex implements plugin.RegistryFetcher.
func (f HTTPFetcher) FetchIndex(ctx context.Context) ([]byte, error) {
	return f.get(ctx, f.BaseURL+"index.json")
}

// FetchArtifact implements plugin.RegistryFetcher.
func (f HTTPFetcher) FetchArtifact(ctx context.Context, entry plugin.RegistryVersionEntry) ([]byte, error) {
	if entry.DownloadURL == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "registryfetch: entry has no download url")
	}
	return f.get(ctx, entry.DownloadURL)
}

func (f HTTPFetcher) get(ctx context.Context, url string) ([]byte, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	doer := f.Doer
	if doer == nil {
		doer = realDoer{}
	}
	resp, err := doer.Do(reqCtx, HTTPRequest{URL: url})
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "registryfetch: GET %s", url)
	}
	if resp.StatusCode != 200 {
		return nil, cascade.Wrapf(cascade.KindUnavailable, plugin.ErrRegistryHTTP, "GET %s: status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}
