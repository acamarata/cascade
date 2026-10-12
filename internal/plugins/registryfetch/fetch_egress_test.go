//go:build integration

package registryfetch_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins/registryfetch"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestRegistryFetchRefusedClassBlocksFetch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("registry body"))
	}))
	t.Cleanup(srv.Close)
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	reg := egress.NewRegistry()
	reg.MustRegister(egress.EgressClassRegistryFetch, egress.InterceptConfig{Owner: "registry", Enabled: false})
	engine, err := egress.NewEngine(reg, &secrets.EgressVault{}, detector)
	if err != nil {
		t.Fatal(err)
	}
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassRegistryFetch)
	if err != nil {
		t.Fatal(err)
	}
	f := registryfetch.HTTPFetcher{BaseURL: srv.URL + "/", Engine: engine, Capability: token}
	for _, fetch := range fetchBoth(f) {
		data, err := fetch()
		if data != nil || !errors.Is(err, egress.ErrClassDisabled) {
			t.Fatalf("data=%q error=%v, want disabled class and no bytes", data, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("server calls=%d, want 0", calls.Load())
	}
}

func TestRegistryFetchRedirectIsAdmitted(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		_, _ = w.Write([]byte("target body"))
	}))
	t.Cleanup(target.Close)
	clean := target.URL + "/index.json"
	withUser := strings.Replace(target.URL, "://", "://user:"+"token@", 1) + "/index.json"
	for name, location := range map[string]string{"credentials": withUser, "clean": clean} {
		t.Run(name, func(t *testing.T) {
			targetCalls.Store(0)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, location, http.StatusFound)
			}))
			t.Cleanup(origin.Close)
			data, err := newFetcher(nil, origin.URL+"/").FetchIndex(context.Background())
			if name == "clean" {
				if err != nil || string(data) != "target body" || targetCalls.Load() != 1 {
					t.Fatalf("data=%q error=%v target calls=%d, want the admitted redirect followed", data, err, targetCalls.Load())
				}
				return
			}
			if err == nil || data != nil || targetCalls.Load() != 0 {
				t.Fatalf("data=%q error=%v target calls=%d, want refusal before the redirect", data, err, targetCalls.Load())
			}
			if strings.Contains(err.Error(), "token") {
				t.Fatalf("refusal echoes the redirect credentials: %v", err)
			}
		})
	}
}

// valueVault holds one synthetic secret.
type valueVault struct{ value string }

func (valueVault) List(context.Context) ([]string, error) { return []string{"fake"}, nil }
func (v valueVault) Get(context.Context, string) ([]byte, error) {
	return []byte(v.value), nil
}

func spaceFetcher(t *testing.T, d registryfetch.Doer, base, value string) registryfetch.HTTPFetcher {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), valueVault{value}, detector)
	if err != nil {
		t.Fatal(err)
	}
	token, err := engine.Capability(egress.EgressClassRegistryFetch)
	if err != nil {
		t.Fatal(err)
	}
	return registryfetch.HTTPFetcher{BaseURL: base, Doer: d, Engine: engine, Capability: token}
}

// requireRefusedEverywhere checks that target is refused as the initial URL
// (index and artifact, zero Doer calls) and as a redirect hop, without the
// error echoing the secret or the target.
func requireRefusedEverywhere(t *testing.T, value, target string, d *fakeDoer, f registryfetch.HTTPFetcher, redirect func(string, int) error) {
	t.Helper()
	check := func(what string, data []byte, err error) {
		if err == nil || data != nil || d.sawURL != "" {
			t.Fatalf("%s: data=%q error=%v network=%q, want refusal before the transport", what, data, err, d.sawURL)
		}
		if msg := err.Error(); strings.Contains(msg, value) || strings.Contains(msg, target) {
			t.Fatalf("%s: refusal echoes the secret or the url: %v", what, err)
		}
	}
	for i, fetch := range fetchBoth(f) {
		data, err := fetch()
		check([]string{"index", "artifact"}[i], data, err)
	}
	data, err := f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{DownloadURL: target})
	check("download url", data, err)
	check("redirect", nil, redirect(target, 0))
}

// encContext is one URL position: esc is the natural escape for it, wrap
// builds a URL ending in a separator so a suffix stays harmless.
type encContext struct {
	name string
	esc  func(string) string
	wrap func(enc string) string
}

func pct(r rune) string {
	var b strings.Builder
	for _, c := range []byte(string(r)) {
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

var encContexts = []encContext{
	{"query-value", neturl.QueryEscape, func(e string) string { return "https://registry.example/?t=" + e + "&x=" }},
	{"path", neturl.PathEscape, func(e string) string { return "https://registry.example/" + e + "/" }},
	{"query-key", neturl.QueryEscape, func(e string) string { return "https://registry.example/?" + e + "=1&x=" }},
	{"fragment", neturl.PathEscape, func(e string) string { return "https://registry.example/#" + e + "/" }},
	{"double-layer", neturl.QueryEscape, func(e string) string {
		return "https://registry.example/?t=" + neturl.QueryEscape(e) + "&x="
	}},
}

// masks lists which runes take the second encoding: uniform, single flips,
// alternating, and fixed-seed random.
func masks(n int) [][]bool {
	out := [][]bool{make([]bool, n), make([]bool, n)}
	for i := range out[1] {
		out[1][i] = true
	}
	for base := 0; base < 2; base++ {
		for i := 0; i < n; i++ {
			m := append([]bool(nil), out[base]...)
			m[i] = !m[i]
			out = append(out, m)
		}
	}
	// Two alternating masks, then 64 fixed-seed random ones.
	rng := rand.New(rand.NewSource(26))
	for k := 0; k < 66; k++ {
		m := make([]bool, n)
		for i := range m {
			m[i] = (k < 2 && (i+k)%2 == 0) || (k >= 2 && rng.Intn(2) == 1)
		}
		out = append(out, m)
	}
	return out
}

func mixEncode(v string, esc func(string) string, mask []bool) string {
	var b strings.Builder
	for i, r := range []rune(v) {
		if mask[i] {
			b.WriteString(pct(r))
		} else {
			b.WriteString(esc(string(r)))
		}
	}
	return b.String()
}

// readsAsSecret reports whether a server would read value from rawURL: some
// decoded path, fragment, query key or query value (or its further decoding)
// contains it. It keeps the differential cases from being vacuous.
func readsAsSecret(t *testing.T, rawURL, value string) bool {
	t.Helper()
	u, err := neturl.Parse(rawURL)
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	q, err := neturl.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatalf("fixture query does not parse: %v", err)
	}
	parts := []string{u.Path, u.Fragment}
	for k, vs := range q {
		parts = append(parts, append([]string{k}, vs...)...)
	}
	for _, p := range parts {
		again, _ := neturl.QueryUnescape(p)
		if strings.Contains(p, value) || strings.Contains(again, value) {
			return true
		}
	}
	return false
}

func TestRegistryFetchRefusesEveryEncodingOfSecret(t *testing.T) {
	values := []string{
		"fake+key value%&=/?#.-é9",
		"fake key+value/9%2B?&=",
		"x#fake.-pass word+é%41=&",
		"fake%20plus+and space.9-#",
	}
	for vi, v := range values {
		// url.Values.Encode is QueryEscape per key and value, so query-value covers it.
		if got := (neturl.Values{"t": {v}}).Encode(); got != "t="+neturl.QueryEscape(v) {
			t.Fatalf("Values.Encode form %q is not covered", got)
		}
		for _, c := range encContexts {
			t.Run(fmt.Sprintf("value%d/%s", vi, c.name), func(t *testing.T) {
				d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("registry body")}}
				cleanFetch := spaceFetcher(t, d, "https://registry.example/", v)
				if _, err := cleanFetch.FetchIndex(context.Background()); err != nil || d.redirect == nil {
					t.Fatalf("clean fetch error=%v, redirect check supplied=%v", err, d.redirect != nil)
				}
				redirect := d.redirect
				for _, mask := range masks(len([]rune(v))) {
					base := c.wrap(mixEncode(v, c.esc, mask))
					if !readsAsSecret(t, base, v) {
						t.Fatalf("mask %v: the url does not carry the secret to a server", mask)
					}
					d.sawURL = ""
					f := spaceFetcher(t, d, base, v)
					requireRefusedEverywhere(t, v, base, d, f, redirect)
				}
			})
		}
	}
}

// TestRegistryFetchPlainControlPasses keeps the differential test honest: a
// plain URL under the same vault is admitted, so the refusals above come
// from the secret's encodings, not from a fetcher that refuses everything.
func TestRegistryFetchPlainControlPasses(t *testing.T) {
	d := &fakeDoer{resp: registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("artifact")}}
	f := spaceFetcher(t, d, "https://registry.example/", "fake+key value%&=/?#.-é9")
	url := "https://cdn.example/a-b/c_d.1~2.tgz"
	if _, err := f.FetchArtifact(context.Background(), plugin.RegistryVersionEntry{DownloadURL: url}); err != nil || d.sawURL != url {
		t.Fatalf("error=%v network=%q, want the clean URL unchanged", err, d.sawURL)
	}
}

// leakyRedirectDoer errs like net/http, quoting the refused Location.
type leakyRedirectDoer struct{ location string }

func (d leakyRedirectDoer) Do(_ context.Context, req registryfetch.HTTPRequest) (registryfetch.HTTPResponse, error) {
	if err := req.CheckRedirect(d.location, 1); err != nil {
		return registryfetch.HTTPResponse{}, errors.New("Get \"" + d.location + "\": " + err.Error())
	}
	return registryfetch.HTTPResponse{StatusCode: 200, Body: []byte("followed")}, nil
}

func TestRegistryFetchRedirectRefusalHidesLocation(t *testing.T) {
	for _, location := range []string{"https://user:" + "token@cdn.example/x", "https://cdn.example/?t=" + fakeQuerySecret + "&"} {
		data, err := newFetcher(leakyRedirectDoer{location: location}, "https://registry.example/").FetchIndex(context.Background())
		if err == nil || data != nil || !cascade.HasKind(err, cascade.KindPolicyDenied) || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), fakeQuerySecret) {
			t.Fatalf("data=%q error=%v, want a policy refusal that hides the redirect target", data, err)
		}
	}
}

func TestRegistryFetchUnparsableLocationHidden(t *testing.T) {
	location := "https://u:" + "hunter2@cdn.example/%zz?t=" + fakeQuerySecret
	d := &fakeDoer{failErr: errors.New("Get \"https://registry.example/index.json\": failed to parse Location header \"" + location + "\": invalid URL escape")}
	_, err := newFetcher(d, "https://registry.example/").FetchIndex(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), fakeQuerySecret) {
		t.Fatalf("error = %v, want KindUnavailable that hides the unparsable location", err)
	}
}
