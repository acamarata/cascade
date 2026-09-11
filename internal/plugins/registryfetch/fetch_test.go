package registryfetch_test

// Purpose: unit tests for HTTPFetcher over an in-memory fakeDoer — no
//   net/http import in this file at all (see fetch.go's header comment),
//   so this file carries no build tag and runs in the default unit lane.
// SPORT: internal/plugins/registryfetch (ADD) — P1-E24-W5-S50-T1.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"

	"github.com/acamarata/cascade/internal/plugins/registryfetch"
)

// fakeDoer is a recording, in-memory registryfetch.Doer (no net/http
// import; Art.7.2 — same pattern internal/providers/intake's
// transport_test.go documents for its own fakeDoer).
type fakeDoer struct {
	resp    registryfetch.HTTPResponse
	sawURL  string
	failErr error
}

func (f *fakeDoer) Do(_ context.Context, req registryfetch.HTTPRequest) (registryfetch.HTTPResponse, error) {
	f.sawURL = req.URL
	if f.failErr != nil {
		return registryfetch.HTTPResponse{}, f.failErr
	}
	return f.resp, nil
}

func newFetcher(d registryfetch.Doer, base string) registryfetch.HTTPFetcher {
	return registryfetch.HTTPFetcher{BaseURL: base, Doer: d}
}

func TestHTTPFetcher_FetchIndexOK(t *testing.T) {
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte(`{"schema_version":"1","entries":[]}`)}}
	f := newFetcher(d, "https://registry.example.com/")

	data, err := f.FetchIndex(context.Background())
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if string(data) != `{"schema_version":"1","entries":[]}` {
		t.Fatalf("FetchIndex body = %q", data)
	}
	if d.sawURL != "https://registry.example.com/index.json" {
		t.Fatalf("FetchIndex requested %q, want the index.json URL", d.sawURL)
	}
}

func TestHTTPFetcher_FetchIndexNon200(t *testing.T) {
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 404, Body: []byte("not found")}}
	f := newFetcher(d, "https://registry.example.com/")

	_, err := f.FetchIndex(context.Background())
	if err == nil {
		t.Fatal("FetchIndex: want error on 404, got nil")
	}
	if !errors.Is(err, plugin.ErrRegistryHTTP) {
		t.Fatalf("FetchIndex error = %v, want errors.Is(err, plugin.ErrRegistryHTTP)", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("FetchIndex error kind = %v, want KindUnavailable", err)
	}
}

func TestHTTPFetcher_FetchIndexTransportError(t *testing.T) {
	d := &fakeDoer{failErr: errors.New("connection refused")}
	f := newFetcher(d, "https://registry.example.com/")

	_, err := f.FetchIndex(context.Background())
	if err == nil {
		t.Fatal("FetchIndex: want error on transport failure, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("FetchIndex error kind = %v, want KindUnavailable", err)
	}
}

func TestHTTPFetcher_FetchArtifact(t *testing.T) {
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("artifact-bytes")}}
	f := newFetcher(d, "https://registry.example.com/")

	entry := plugin.RegistryVersionEntry{DownloadURL: "https://cdn.example.com/a.wasm"}
	data, err := f.FetchArtifact(context.Background(), entry)
	if err != nil {
		t.Fatalf("FetchArtifact: %v", err)
	}
	if string(data) != "artifact-bytes" {
		t.Fatalf("FetchArtifact = %q", data)
	}
	if d.sawURL != entry.DownloadURL {
		t.Fatalf("FetchArtifact requested %q, want %q", d.sawURL, entry.DownloadURL)
	}
}

func TestHTTPFetcher_FetchArtifactNoURL(t *testing.T) {
	f := newFetcher(&fakeDoer{}, "https://registry.example.com/")

	_, err := f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("FetchArtifact error = %v, want KindInvalidInput", err)
	}
}

// TestHTTPFetcher_DefaultDoerRequestBuildError exercises the production
// realDoer (doer.go) with no Doer injected — the only slice of realDoer
// this default lane can safely reach: a malformed URL fails inside
// http.NewRequestWithContext before any socket is opened, so this stays
// within the no-network-unit-lane rule (no real connection attempt) while
// still proving the nil-Doer fallback wires to the real implementation,
// not a silently-broken stub. realDoer's successful-request/socket path
// is covered separately, under the integration build tag
// (doer_integration_test.go), per AGENT-BRIEF.md.
func TestHTTPFetcher_DefaultDoerRequestBuildError(t *testing.T) {
	f := registryfetch.HTTPFetcher{BaseURL: "://malformed-url-no-scheme"}
	_, err := f.FetchIndex(context.Background())
	// The Doer abstraction deliberately does not distinguish "malformed
	// URL" from any other Do failure at the HTTPFetcher.get layer (both
	// are, from get's perspective, "the Doer could not produce a
	// response") -- get wraps every Doer error as KindUnavailable. What
	// this test proves is narrower and load-bearing on its own: the
	// nil-Doer fallback reaches the real realDoer implementation (whose
	// request-build failure surfaces as a non-nil error here) rather than
	// silently succeeding or panicking on a nil interface value.
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("FetchIndex with malformed URL and default Doer error = %v, want KindUnavailable", err)
	}
}

// TestHTTPFetcher_DefaultDoerContextAlreadyCanceled exercises realDoer's
// http.DefaultClient.Do call itself with a context that is already
// canceled before Do is reached: net/http's transport checks
// ctx.Done() before dialing, so this fails immediately with no socket
// ever opened, closing the remaining default-lane gap in Do's coverage
// without violating the no-real-connection intent behind the
// no-network-unit-lane rule.
func TestHTTPFetcher_DefaultDoerContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := registryfetch.HTTPFetcher{BaseURL: "https://registry.example.com/"}
	_, err := f.FetchIndex(ctx)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("FetchIndex with canceled context and default Doer error = %v, want KindUnavailable", err)
	}
}
