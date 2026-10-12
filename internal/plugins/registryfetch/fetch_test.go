package registryfetch_test

// Purpose: unit tests for HTTPFetcher over an in-memory fakeDoer, with no
//   net/http import, so this file runs untagged in the default unit lane.
// SPORT: internal/plugins/registryfetch (ADD) — P1-E24-W5-S50-T1.

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"

	"github.com/acamarata/cascade/internal/plugins/registryfetch"
)

// fakeDoer is a recording, in-memory registryfetch.Doer (Art.7.2).
type fakeDoer struct {
	resp     registryfetch.HTTPResponse
	sawURL   string
	failErr  error
	redirect func(string, int) error
}

func (f *fakeDoer) Do(_ context.Context, req registryfetch.HTTPRequest) (registryfetch.HTTPResponse, error) {
	f.sawURL = req.URL
	f.redirect = req.CheckRedirect
	if f.failErr != nil {
		return registryfetch.HTTPResponse{}, f.failErr
	}
	return f.resp, nil
}

// fakeVault holds synthetic stored values, name to value.
type fakeVault map[string]string

func (v fakeVault) List(context.Context) ([]string, error) { return slices.Collect(maps.Keys(v)), nil }

func (v fakeVault) Get(_ context.Context, name string) ([]byte, error) {
	if value, ok := v[name]; ok {
		return []byte(value), nil
	}
	return nil, errors.New("no stored value")
}

func newFetcher(d registryfetch.Doer, base string) registryfetch.HTTPFetcher {
	return vaultFetcher(d, base, fakeVault{})
}

func vaultFetcher(d registryfetch.Doer, base string, vault fakeVault) registryfetch.HTTPFetcher {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		panic(err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		panic(err)
	}
	token, err := engine.Capability(egress.EgressClassRegistryFetch)
	if err != nil {
		panic(err)
	}
	return registryfetch.HTTPFetcher{BaseURL: base, Doer: d, Engine: engine, Capability: token}
}

// fetchBoth returns an index fetch and an artifact fetch under f.BaseURL.
func fetchBoth(f registryfetch.HTTPFetcher) []func() ([]byte, error) {
	return []func() ([]byte, error){
		func() ([]byte, error) { return f.FetchIndex(context.Background()) },
		func() ([]byte, error) {
			return f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{DownloadURL: f.BaseURL + "artifact"})
		},
	}
}

func TestRegistryFetchRefusedClassBlocksFetchUnit(t *testing.T) {
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("registry body")}}
	f := newFetcher(d, "https://registry.example/")
	reg := egress.NewRegistry()
	reg.MustRegister(egress.EgressClassRegistryFetch, egress.InterceptConfig{Owner: "registry", Enabled: false})
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	if f.Engine, err = egress.NewEngine(reg, fakeVault{}, detector); err != nil {
		t.Fatal(err)
	}
	for _, fetch := range fetchBoth(f) {
		body, err := fetch()
		if body != nil || !errors.Is(err, egress.ErrClassDisabled) || d.sawURL != "" {
			t.Fatalf("body=%q error=%v network=%q, want refusal before transport", body, err, d.sawURL)
		}
	}
}

func TestRegistryFetchPreservesSecretLookingBody(t *testing.T) {
	body := []byte("AKIA" + "IOSFODNN7EXAMPLE\n" + "ghp_" + strings.Repeat("a", 36) +
		"\n-----BEGIN " + "PRIVATE KEY-----\nZml4dHVyZQ==\n-----END " + "PRIVATE KEY-----\n")
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: body}}
	for _, fetch := range fetchBoth(newFetcher(d, "https://registry.example/")) {
		got, err := fetch()
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("body=%q error=%v, want byte-identical fixture", got, err)
		}
	}
}

// Synthetic, obviously fake secret shapes; split so no scanner sees a literal.
var fakeQuerySecret = "ghp_" + strings.Repeat("a", 36)

// encodeAll percent-encodes every byte of s, layers times over.
func encodeAll(s string, layers int) string {
	for range layers {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{s[i]})))
		}
		s = b.String()
	}
	return s
}

// requireRefused checks that target is refused as a download URL and as a
// redirect hop, with zero Doer calls and an error that echoes no secret.
func requireRefused(t *testing.T, vault fakeVault, target string) {
	t.Helper()
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("registry body")}}
	f := vaultFetcher(d, "https://registry.example/", vault)
	if _, err := f.FetchIndex(context.Background()); err != nil || d.redirect == nil {
		t.Fatalf("clean fetch error=%v, redirect check supplied=%v", err, d.redirect != nil)
	}
	d.sawURL = ""
	data, err := f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{DownloadURL: target})
	for i, err := range []error{err, d.redirect(target, 0)} {
		if err == nil || data != nil || d.sawURL != "" || !cascade.HasKind(err, cascade.KindPolicyDenied) && !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%q (redirect=%v): data=%q error=%v network=%q, want refusal before the transport", target, i == 1, data, err, d.sawURL)
		}
		for _, leak := range append([]string{target, "token", fakeQuerySecret}, slices.Collect(maps.Values(vault))...) {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("refusal echoes %q: %v", leak, err)
			}
		}
	}
}

// TestRegistryFetchStructuralRule refuses every URL outside the structural
// rule: https (http only to loopback), ASCII host, no userinfo, query or
// fragment, and a plain path with no empty or dot segment.
func TestRegistryFetchStructuralRule(t *testing.T) {
	for name, target := range map[string]string{
		"http-remote": "http://cdn.example/a.tgz", "http-localish": "http://localhost/a.tgz",
		"ftp": "ftp://cdn.example/a.tgz", "opaque": "https:cdn.example",
		"no-host": "https:///a.tgz", "userinfo": "https://user:" + "token@cdn.example/",
		"short-pass": "https://u:" + "p@cdn.example/", "username-only": "https://user@cdn.example/",
		"query": "https://cdn.example/a.tgz?v=1", "empty-query": "https://cdn.example/a.tgz?",
		"query-secret": "https://cdn.example/?t=" + fakeQuerySecret, "fragment": "https://cdn.example/a.tgz#f",
		"empty-frag": "https://cdn.example/a.tgz#", "escape": "https://cdn.example/a%20b.tgz",
		"escaped-plain": "https://cdn.example/" + encodeAll("plain", 1), "double-escape": "https://cdn.example/" + encodeAll(fakeQuerySecret, 2) + "/",
		"unparsable": "https://cdn.example/%zz/", "plus": "https://cdn.example/a+b.tgz",
		"angle": "https://cdn.example/a<b>.tgz", "at": "https://cdn.example/a@b",
		"space": "https://cdn.example/a b", "dot": "https://cdn.example/a/./b",
		"dot-dot": "https://cdn.example/a/../b", "leading-dot": "https://cdn.example/./b",
		"ipv6-zone": "https://[2001:db8::1234%25lo0]/a", "ipv6-zone-link": "https://[fe80::1%25en0]/a",
		"empty-segment": "https://cdn.example/a//b", "host-shape": "https://" + fakeQuerySecret + ".example/",
		"path-shape": "https://cdn.example/" + fakeQuerySecret + "/",
	} {
		t.Run(name, func(t *testing.T) { requireRefused(t, fakeVault{}, target) })
	}
}

// TestRegistryFetchBF243Regressions pins the four URL-secret cases the
// structural rule closes, each by its own subtest.
func TestRegistryFetchBF243Regressions(t *testing.T) {
	tagged := "<apikey>fakeapikey12345</apikey>"
	for name, c := range map[string]struct {
		vault   fakeVault
		targets []string
	}{
		// net/http IDNA-maps a non-ASCII host, so the dial would carry the secret.
		"non-ascii-host": {fakeVault{"fake": "fakepassword9"}, []string{"https://ｆａｋｅｐａｓｓｗｏｒｄ９.evil.example/a",
			"https://fake\u00adpassword9.evil.example/a", "https://fake%C2%ADpassword9.evil.example/a"}},
		// A value wrapped in a secret-reference tag is not exempt.
		"tag-wrapped": {fakeVault{"fake": "fakeapikey12345"}, []string{"https://cdn.example/?t=" + tagged,
			"https://cdn.example/?t=" + encodeAll(tagged, 1), "https://cdn.example/" + tagged + "/", "https://cdn.example/" + encodeAll(tagged, 1) + "/"}},
		// Values under the engine's 8-byte floor, down to 4 bytes; the host folds case.
		"short-value": {fakeVault{"six": "fk6pw9", "four": "zq4x"}, []string{"https://cdn.example/?t=fk6pw9", "https://cdn.example/a/fk6pw9.tgz",
			"https://fk6pw9.cdn.example/a.tgz", "https://FK6PW9.cdn.example/a.tgz", "https://cdn.example/zq4x", "https://cdn.example:443/x/zq4x/"}},
		// Servers remove dot segments and merge slashes.
		"dot-segment": {fakeVault{"fake": "fake/pass-word9"}, []string{"https://cdn.example/fake/x/../pass-word9/a",
			"https://cdn.example/fake/x/%2e%2e/pass-word9/a", "https://cdn.example/fake//pass-word9/a", "https://cdn.example/fake/./pass-word9/a"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, target := range c.targets {
				requireRefused(t, c.vault, target)
			}
		})
	}
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("ok")}}
	for vault, target := range map[string]string{"zq4": "https://cdn.example/zq4x", "fk6pw9": "https://cdn.example/FK6PW9.tgz"} {
		f := vaultFetcher(d, "https://registry.example/", fakeVault{"v": vault})
		if _, err := f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{DownloadURL: target}); err != nil {
			t.Fatalf("a 3-byte value or a case-folded path refused %q: %v", target, err)
		}
	}
}

func TestRegistryFetchAdmitsPlainURLs(t *testing.T) {
	for _, target := range []string{"https://cdn.example/a/b_c-1.2~x.tgz", "https://cdn.example:8443/dir/", "https://cdn.example", "http://127.0.0.1:8080/index.json", "http://[::1]:8080/a"} {
		d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("artifact")}}
		f := vaultFetcher(d, "https://registry.example/", fakeVault{"fake": "fakepassword9"})
		if _, err := f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{DownloadURL: target}); err != nil || d.sawURL != target {
			t.Fatalf("%q: error=%v network=%q, want the URL admitted unchanged", target, err, d.sawURL)
		}
	}
}

func TestRegistryFetchAdmitsEveryRedirect(t *testing.T) {
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("registry body")}}
	if _, err := newFetcher(d, "https://registry.example/").FetchIndex(context.Background()); err != nil || d.redirect == nil {
		t.Fatalf("error=%v, redirect check supplied=%v", err, d.redirect != nil)
	}
	if err := d.redirect("https://cdn.example/index.json", 9); err != nil {
		t.Fatalf("clean redirect refused: %v", err)
	}
	if err := d.redirect("https://cdn.example/index.json", 10); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("redirect past the hop limit = %v, want KindUnavailable", err)
	}
}

func TestRegistryFetchUnconfiguredRefusesBeforeNetwork(t *testing.T) {
	for name, mutate := range map[string]func(*registryfetch.HTTPFetcher) error{
		"zero":          func(f *registryfetch.HTTPFetcher) error { *f = registryfetch.HTTPFetcher{Doer: f.Doer}; return nil },
		"nil-engine":    func(f *registryfetch.HTTPFetcher) error { f.Engine = nil; return nil },
		"zero-engine":   func(f *registryfetch.HTTPFetcher) error { f.Engine = &egress.Engine{}; return nil },
		"no-capability": func(f *registryfetch.HTTPFetcher) error { f.Capability = egress.Capability{}; return nil },
		"wrong-class": func(f *registryfetch.HTTPFetcher) (err error) {
			f.Capability, err = f.Engine.Capability(egress.EgressClassMCP)
			return err
		},
	} {
		d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("unfiltered")}}
		f := newFetcher(d, "https://registry.example/")
		if err := mutate(&f); err != nil {
			t.Fatal(err)
		}
		for _, fetch := range fetchBoth(f) {
			if data, err := fetch(); data != nil || !cascade.HasKind(err, cascade.KindUnavailable) || d.sawURL != "" {
				t.Fatalf("%s: body=%q error=%v network=%q", name, data, err, d.sawURL)
			}
		}
	}
}

func TestHTTPFetcherFetchPaths(t *testing.T) {
	ok := func(body string) *fakeDoer {
		return &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	}
	d := ok("index")
	if data, err := newFetcher(d, "https://registry.example/").FetchIndex(context.Background()); err != nil || string(data) != "index" || d.sawURL != "https://registry.example/index.json" {
		t.Fatalf("FetchIndex body=%q error=%v requested=%q", data, err, d.sawURL)
	}
	d = ok("artifact-bytes")
	entry := plugin.RegistryVersionEntry{DownloadURL: "https://cdn.example/a.wasm"}
	if data, err := newFetcher(d, "https://registry.example/").FetchArtifact(context.Background(), entry); err != nil || string(data) != "artifact-bytes" || d.sawURL != entry.DownloadURL {
		t.Fatalf("FetchArtifact body=%q error=%v requested=%q", data, err, d.sawURL)
	}
	if _, err := newFetcher(ok(""), "https://registry.example/").FetchArtifact(context.Background(), plugin.RegistryVersionEntry{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("FetchArtifact without a url error = %v, want KindInvalidInput", err)
	}
	if _, err := newFetcher(nil, "://malformed-url-no-scheme").FetchIndex(context.Background()); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("malformed URL error = %v, want KindInvalidInput", err)
	}
	for name, d := range map[string]*fakeDoer{
		"non-200":   {resp: registryfetch.HTTPResponse{StatusCode: 404, Body: []byte("not found")}},
		"transport": {failErr: errors.New("connection refused")},
	} {
		_, err := newFetcher(d, "https://registry.example/").FetchIndex(context.Background())
		if !cascade.HasKind(err, cascade.KindUnavailable) || (name == "non-200" && !errors.Is(err, plugin.ErrRegistryHTTP)) {
			t.Fatalf("%s: FetchIndex error = %v, want KindUnavailable", name, err)
		}
	}
	// A canceled context reaches realDoer: net/http checks ctx.Done() before
	// dialing, so no socket opens in the unit lane.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newFetcher(nil, "https://registry.example/").FetchIndex(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("canceled context error = %v, want KindUnavailable", err)
	}
}
