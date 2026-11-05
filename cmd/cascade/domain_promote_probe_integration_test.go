//go:build integration

// Purpose: the registry half of the domain promote probe (P1-TOP-19d)
// through the real dispatch Resolver, drivers and shared transport.
// Inputs: loopback fake providers and an in-memory registry view.
// Outputs: none.
// Constraints: loopback only; the credential value is built at runtime.
//
// SPORT: cmd/cascade domain_promote_probe/ADD (P1-TOP-19d).
package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	providertransport "github.com/acamarata/cascade/providers/transport"
)

// probeValue is the runtime-built bearer value the fakes expect.
var probeValue = "probe-" + strings.Repeat("v", 8)

// memLookup is an in-memory registry view; tests call it sequentially.
type memLookup struct {
	provs map[string]providerregistry.ProviderRecord
	lanes []providerregistry.LaneRecord
}

func (m *memLookup) ListLanes(context.Context) ([]providerregistry.LaneRecord, error) {
	return append([]providerregistry.LaneRecord(nil), m.lanes...), nil
}

func (m *memLookup) GetProvider(_ context.Context, name string) (providerregistry.ProviderRecord, error) {
	rec, ok := m.provs[name]
	if !ok {
		return rec, cascade.Newf(cascade.KindNotFound, "provider %q not found", name)
	}
	return rec, nil
}

func (m *memLookup) UpsertLane(context.Context, providerregistry.LaneRecord) error { return nil }

// add registers one provider and one lane for it.
func (m *memLookup) add(rec providerregistry.ProviderRecord) {
	m.provs[rec.Name] = rec
	m.lanes = append(m.lanes, providerregistry.LaneRecord{LaneName: rec.Name + "-lane", ProviderName: rec.Name,
		Weight: 1, Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable})
}

// countingCreds answers every reference with probeValue and counts calls.
type countingCreds struct{ n atomic.Int32 }

func (c *countingCreds) Resolve(context.Context, string) (string, error) {
	c.n.Add(1)
	return probeValue, nil
}

// fakeProvider serves h, counting requests and bearer key headers.
type fakeProvider struct {
	srv        *httptest.Server
	calls, key atomic.Int32
}

func newFake(t *testing.T, h http.HandlerFunc) *fakeProvider {
	t.Helper()
	f := &fakeProvider{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // so a client cancel reaches r.Context()
		f.calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer "+probeValue {
			f.key.Add(1)
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// status answers code; body answers s as JSON.
func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s))
	}
}

const oneToken = `{"model":"m","message":{"role":"assistant","content":"p"},"done":true,"eval_count":1}`

// okReply answers one token, but 401s without the key header.
func okReply(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+probeValue {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body(oneToken)(w, r)
}

// rec is an Ollama provider record at url.
func rec(name, url, ref string) providerregistry.ProviderRecord {
	return providerregistry.ProviderRecord{Name: name, Driver: providerregistry.DriverOllama, BaseURL: url,
		Auth: providerregistry.AuthKey, AuthRef: providerregistry.VaultKeyRef(ref), KnownModels: []string{"m"}}
}

// newProbe builds the probe over one resolver on m.
func newProbe(t *testing.T, m *memLookup, creds providerdispatch.CredentialSource, opts ...providerdispatch.ResolverOption) (*promoteProbe, *providerdispatch.Resolver) {
	t.Helper()
	clock := runtime.NewSystemClock()
	res, err := providerdispatch.NewResolver(m, creds, clock, providertransport.NewHTTPTransport(&http.Client{}), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return &promoteProbe{lookup: m, resolve: res, clock: clock, by: "operator"}, res
}

// failingRegistry seeds one provider per refusal and returns name -> check.
func failingRegistry(t *testing.T, m *memLookup, target string) map[string]string {
	stall := func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	redirect := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target+r.URL.Path, http.StatusFound) }
	handlers := map[string]http.HandlerFunc{"p401": status(401), "p403": status(403), "p404": status(404),
		"p400": status(400), "p500": status(500), "p302": redirect, "stall": stall, "nonjson": body("not json"), "empty": body("{}")}
	checks := map[string]string{"empty": "reply", "refused": "chat", "nogrant": "resolve", "nolane": "no lane",
		"oauth": "oauth", "nomodel": "no model"}
	for name, h := range handlers {
		m.add(rec(name, newFake(t, h).srv.URL, "vault-"+name))
		if checks[name] == "" {
			checks[name] = "chat"
		}
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	m.add(rec("refused", closed.URL, "vault-refused"))
	m.add(providerregistry.ProviderRecord{Name: "nogrant", Driver: providerregistry.DriverAnthropic, BaseURL: target,
		Auth: providerregistry.AuthGateway, AuthRef: "gateway:acct", KnownModels: []string{"m"}})
	m.provs["nolane"] = rec("nolane", target, "vault-nolane")
	oauth, nomodel := rec("oauth", target, "vault-oauth"), rec("nomodel", target, "vault-nomodel")
	oauth.Auth, nomodel.KnownModels = providerregistry.AuthOAuth, nil
	m.add(oauth)
	m.add(nomodel)
	return checks
}

func TestPromoteProbeFailsClosed(t *testing.T) {
	old := promoteProbeTimeout
	promoteProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { promoteProbeTimeout = old })
	target := newFake(t, okReply)
	m := &memLookup{provs: map[string]providerregistry.ProviderRecord{}}
	checks := failingRegistry(t, m, target.srv.URL)
	m.add(rec("good", target.srv.URL, "vault-good"))
	creds := &countingCreds{}
	p, _ := newProbe(t, m, creds)
	ctx, domain := context.Background(), topology.DomainID("d1")
	for name, check := range checks {
		ev, err := p.probeRegistry(ctx, domain, topology.EvidenceKey{Provider: name})
		k, _ := cascade.KindOf(err)
		if err == nil || k != cascade.KindUnavailable || ev != (topology.BillingEvidence{}) {
			t.Fatalf("%s: probe = %+v, %v; want KindUnavailable and no evidence", name, ev, err)
		}
		if msg := err.Error(); !strings.Contains(msg, `"`+name+`"`) || !strings.Contains(msg, `"`+check+`"`) {
			t.Fatalf("%s: error %q does not name the provider and check %q", name, msg, check)
		}
	}
	if n := target.calls.Load(); n != 0 {
		t.Fatalf("redirect target and refused-before-send providers saw %d requests, want 0", n)
	}
	ev, err := p.probeRegistry(ctx, domain, topology.EvidenceKey{Provider: "good"})
	if err != nil || ev.ProbeKind != promoteKindChat || ev.ProbeStatus != 200 || ev.ProbeEndpoint != target.srv.URL ||
		ev.TestOutcome != topology.EvidenceOutcomePassed || ev.DomainID != domain || ev.Key.Provider != "good" {
		t.Fatalf("good probe = %+v, %v; want one passed chat row", ev, err)
	}
	if target.calls.Load() != 1 || target.key.Load() != 1 {
		t.Fatalf("good provider: %d requests, %d with the key header; want 1 and 1", target.calls.Load(), target.key.Load())
	}
	keyless := newFake(t, body(oneToken))
	m.add(rec("keyless", keyless.srv.URL, ""))
	before := creds.n.Load()
	if _, err := p.probeRegistry(ctx, domain, topology.EvidenceKey{Provider: "keyless"}); err != nil {
		t.Fatalf("keyless probe: %v", err)
	}
	if creds.n.Load() != before || keyless.key.Load() != 0 || keyless.calls.Load() != 1 {
		t.Fatalf("keyless probe used a credential (%d lookups, %d key headers)", creds.n.Load()-before, keyless.key.Load())
	}
}

// demotions records DemoteProvider calls.
type demotions struct {
	provider.HealthManager
	n atomic.Int32
}

func (d *demotions) DemoteProvider(context.Context, string, provider.DemotionReason) error {
	d.n.Add(1)
	return nil
}

func TestPromoteProbeDoesNotFeedDemotion(t *testing.T) {
	// The anthropic driver is the one that marks a 500 transient.
	var fail atomic.Bool
	fail.Store(true)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() || r.Header.Get("X-Api-Key") != probeValue {
			w.WriteHeader(map[bool]int{true: 500, false: 401}[fail.Load()])
			return
		}
		body(`{"type":"message","role":"assistant","content":[{"type":"text","text":"p"}],"usage":{"output_tokens":1}}`)(w, r)
	})
	m := &memLookup{provs: map[string]providerregistry.ProviderRecord{}}
	p0 := rec("p", f.srv.URL, "vault-p")
	p0.Driver = providerregistry.DriverAnthropic
	m.add(p0)
	h := &demotions{}
	p, res := newProbe(t, m, &countingCreds{}, providerdispatch.WithHealth(h))
	ctx := context.Background()
	call := func() {
		mp, err := res.Resolve(ctx, provider.Selection{LaneID: "p-lane", Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = mp.Chat(ctx, provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "x"}}})
	}
	call()
	call()
	for i := 0; i < 3; i++ {
		if _, err := p.probeRegistry(ctx, "d", topology.EvidenceKey{Provider: "p"}); err == nil {
			t.Fatal("a 500 probe passed")
		}
	}
	if n := h.n.Load(); n != 0 {
		t.Fatalf("demotions after three 500 probes = %d, want 0", n)
	}
	fail.Store(false)
	if _, err := p.probeRegistry(ctx, "d", topology.EvidenceKey{Provider: "p"}); err != nil {
		t.Fatalf("passing probe: %v", err)
	}
	fail.Store(true)
	call()
	if n := h.n.Load(); n != 1 {
		t.Fatalf("demotions after the next transient = %d, want 1 (the probe pass must not reset the count)", n)
	}
	if n := withoutTransientCallers(t); n != 1 {
		t.Fatalf("production WithoutTransientCount callers = %d, want 1", n)
	}
}

// withoutTransientCallers counts non-test WithoutTransientCount call sites
// in the module.
func withoutTransientCallers(t *testing.T) int {
	root, n := filepath.Join("..", ".."), 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "vendor"):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if c, ok := node.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithoutTransientCount" {
					n++
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
