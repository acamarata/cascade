package update

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSelfUpdateHonoursBaseURL is acceptance [6]: with
// CASCADE_UPDATE_BASE_URL at a loopback server serving a signed dist, the
// update succeeds and every fetched URL is under that base.
func TestSelfUpdateHonoursBaseURL(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	base, log := startRelease(t, f.dist, "v2.0.1")
	src, err := SourceFromEnv(func(k string) string { return map[string]string{EnvBaseURL: base + "/"}[k] })
	must(t, err)
	ic := &countingInterceptor{}
	d := f.deps("2.0.0", HTTPFetcher{Interceptor: ic, BaseURL: src.BaseURL})
	d.Source = src
	res, err := Apply(context.Background(), d)
	must(t, err)
	if !res.Updated || f.read("cascade") != "cascade 2.0.1" || res.Latest.ReleaseTag != "v2.0.1" {
		t.Fatalf("apply over the base URL = %+v", res)
	}
	u, _ := url.Parse(base)
	if res.BaseURLHost != u.Host {
		t.Fatalf("base_url_host = %q, want %q", res.BaseURLHost, u.Host)
	}
	calls := ic.recorded()
	if len(calls) != 6 {
		t.Fatalf("intercepted %d requests, want 6 (latest, checksums and signature twice, archive): %+v", len(calls), calls)
	}
	for _, c := range calls {
		if !strings.HasPrefix(c.content, base+"/") {
			t.Fatalf("fetched %s, not under %s", c.content, base)
		}
	}
	for _, p := range log.hits() {
		if !strings.HasPrefix(p, "/releases/") {
			t.Fatalf("server saw %s outside the base path", p)
		}
	}
}

// TestUpdateRequestsTransitEgressClass is acceptance [14]: one
// InterceptClass call per request, redirects included; a refusing, nil or
// rewriting interceptor refuses before any connection.
func TestUpdateRequestsTransitEgressClass(t *testing.T) {
	dist := t.TempDir()
	writeDist(t, dist, distSpec{version: "2.0.1", key: newTestKey(1)})
	base, log := startRelease(t, dist, "v2.0.1")
	hop := "/releases/download/v2.0.1/hop"
	log.redirects[hop] = base + "/download/v2.0.1/" + checksumsName
	ic := &countingInterceptor{}
	rc, err := HTTPFetcher{Interceptor: ic, BaseURL: base}.Get(context.Background(), base+"/download/v2.0.1/hop")
	must(t, err)
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
	calls := ic.recorded()
	want := []string{base + "/download/v2.0.1/hop", base + "/download/v2.0.1/" + checksumsName}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want one per request including the redirect", calls)
	}
	for i, c := range calls {
		if c.class != egress.EgressClassReleaseDownload || c.tier != egress.TierPublic || c.content != want[i] {
			t.Fatalf("call %d = %+v, want release-download/public/%s", i, c, want[i])
		}
	}
	sentinel := errors.New("firewall refused")
	before := len(log.hits())
	refusing := []struct {
		name string
		ic   Interceptor
		kind cascade.Kind
	}{
		{"nil interceptor", nil, cascade.KindUnavailable},
		{"rewriting interceptor", &countingInterceptor{rewrite: func([]byte) []byte { return []byte("x") }}, cascade.KindPolicyDenied},
	}
	if _, err := (HTTPFetcher{Interceptor: &countingInterceptor{err: sentinel}, BaseURL: base}).Get(context.Background(), base+"/latest"); err != sentinel {
		t.Fatalf("refusing interceptor error = %v, want the interceptor's own error", err)
	}
	for _, tc := range refusing {
		_, err := HTTPFetcher{Interceptor: tc.ic, BaseURL: base}.Get(context.Background(), base+"/latest")
		if !cascade.HasKind(err, tc.kind) {
			t.Fatalf("%s: error = %v, want kind %v", tc.name, err, tc.kind)
		}
	}
	if after := len(log.hits()); after != before {
		t.Fatalf("server hit %d times by refused requests, want 0", after-before)
	}
}

// TestRedirectRewriteToleratedOnlyForSignedAssetHosts: a rewritten
// redirect target is requested as issued only on the signed asset hosts;
// a rewritten redirect to the base URL host or github.com refuses before
// it is requested, as does a rewrite of a URL this package built.
func TestRedirectRewriteToleratedOnlyForSignedAssetHosts(t *testing.T) {
	dist := t.TempDir()
	writeDist(t, dist, distSpec{version: "2.0.1", key: newTestKey(1)})
	base, log := startRelease(t, dist, "v2.0.1")
	log.mu.Lock()
	log.redirects["/releases/download/v2.0.1/hop"] = base + "/download/v2.0.1/" + checksumsName
	log.mu.Unlock()
	hopOnly := &countingInterceptor{rewrite: func(b []byte) []byte {
		if strings.HasSuffix(string(b), "/hop") {
			return b
		}
		return []byte("redacted")
	}}
	_, err := HTTPFetcher{Interceptor: hopOnly, BaseURL: base}.Get(context.Background(), base+"/download/v2.0.1/hop")
	wantKind(t, err, cascade.KindPolicyDenied, "that the egress firewall rewrote")
	if hits := log.hits(); len(hits) != 1 || len(hopOnly.recorded()) != 2 {
		t.Fatalf("a rewritten redirect to the base host was requested: hits %v", hits)
	}
	rewriting := HTTPFetcher{Interceptor: &countingInterceptor{rewrite: func([]byte) []byte { return []byte("redacted") }}, BaseURL: base}
	for raw, redirect := range map[string]bool{
		"https://objects.githubusercontent.com/a?jwt=x.y.z":        true,
		"https://release-assets.githubusercontent.com/a?sig=abcd1": true,
	} {
		if err := rewriting.admit(context.Background(), raw, redirect); err != nil {
			t.Fatalf("rewritten signed asset redirect %s refused: %v", raw, err)
		}
	}
	for raw, redirect := range map[string]bool{
		"https://github.com/acamarata/cascade/releases/download/v2.0.1/x": true,
		base + "/download/v2.0.1/x":                                       true,
		"https://release-assets.githubusercontent.com/built":              false,
		"https://github.com/acamarata/cascade/releases/latest":            false,
	} {
		wantKind(t, rewriting.admit(context.Background(), raw, redirect), cascade.KindPolicyDenied, "that the egress firewall rewrote")
	}
}

// TestFetcherRefusesUnpinnedHost is acceptance [14]: redirects and requests
// to hosts outside the pinned set are refused before connecting.
func TestFetcherRefusesUnpinnedHost(t *testing.T) {
	dist := t.TempDir()
	base, log := startRelease(t, dist, "v2.0.1")
	log.redirects["/releases/evil"] = "https://evil.example/x"
	log.redirects["/releases/plain"] = "http://github.com/acamarata/cascade"
	log.redirects["/releases/loop"] = base + "/loop"
	ic := &countingInterceptor{}
	fetch := HTTPFetcher{Interceptor: ic, BaseURL: base}
	for _, path := range []string{"/evil", "/plain"} {
		_, err := fetch.Get(context.Background(), base+path)
		wantKind(t, err, cascade.KindPolicyDenied, "unpinned host")
	}
	for _, raw := range []string{"https://example.com/x", "https://user:pw@github.com/x", "https://github.com:8443/x", "https://github.com/x#f"} {
		_, err := fetch.Get(context.Background(), raw)
		wantKind(t, err, cascade.KindPolicyDenied, "refusing a release request")
	}
	if _, err := (HTTPFetcher{Interceptor: ic}).Get(context.Background(), base+"/latest"); !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("plain http with no base URL = %v", err)
	}
	n := len(ic.recorded())
	_, err := fetch.Get(context.Background(), base+"/loop")
	wantKind(t, err, cascade.KindPolicyDenied, "too many redirects")
	if got := len(ic.recorded()) - n; got != 1+maxRedirects {
		t.Fatalf("a redirect loop made %d admitted requests, want %d", got, 1+maxRedirects)
	}
	for _, h := range []string{"https://github.com/a", "https://objects.githubusercontent.com/a?x=1", "https://release-assets.githubusercontent.com/a"} {
		u, _ := url.Parse(h)
		if !(HTTPFetcher{}).hostAllowed(u) {
			t.Fatalf("pinned host %s refused", h)
		}
	}
}

// TestFetcherStatusAndLocate covers non-200 answers and the latest lookup.
func TestFetcherStatusAndLocate(t *testing.T) {
	dist := t.TempDir()
	base, _ := startRelease(t, dist, "v2.0.1")
	fetch := HTTPFetcher{Interceptor: &countingInterceptor{}, BaseURL: base}
	_, err := fetch.Get(context.Background(), base+"/download/v2.0.1/missing")
	wantKind(t, err, cascade.KindUnavailable, "answered 404")
	tag, err := LatestStable(context.Background(), fetch, Source{BaseURL: base})
	if err != nil || tag != "v2.0.1" {
		t.Fatalf("latest = %q, %v", tag, err)
	}
	_, err = fetch.Locate(context.Background(), base+"/download/v2.0.1/missing")
	wantKind(t, err, cascade.KindUnavailable, "did not redirect")
	_, err = fetch.Get(context.Background(), "http://127.0.0.1:1/releases/x")
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("a loopback port other than the base = %v", err)
	}
}
