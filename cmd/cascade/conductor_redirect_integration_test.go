//go:build !windows && integration

// Purpose: production reach of the shared transport's redirect refusal
//
//	(P1-TOP-19): one conductor.execute through the real handler, the real
//	providers registry, the real dispatch Resolver, the real Ollama driver
//	and the transport NewHTTPTransport builds, against a provider base URL
//	that answers 302 to a second local server that would answer 200.
//
// Inputs: the recorded-provider fixture (testdata/recorded-provider) and a
// placeholder bearer value for the local servers.
//
// Outputs: none.
// Constraints: loopback only; the credential source is the only double.
//
// SPORT: cmd/cascade daemon (CHANGE, P1-TOP-19).
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	providertransport "github.com/acamarata/cascade/providers/transport"
)

// redirectRig is one registered conductor.execute whose only provider has
// baseURL as its base URL.
func redirectRig(t *testing.T, baseURL string) *fanRig {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	paths := fakeMemoryPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	reg := seedRecordedRegistry(ctx, t, paths, clock, baseURL)
	cfg, _, err := conductor.ParseQuotaConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := providerdispatch.NewResolver(reg, placeholderCreds{}, clock, providertransport.NewHTTPTransport(&http.Client{}))
	if err != nil {
		t.Fatal(err)
	}
	security, err := conductorSecurity(paths)
	if err != nil {
		t.Fatal(err)
	}
	security.Policy = fileDenyPolicy(ctx, t, paths, clock)
	r := &fanRig{reg: rpc.NewRegistry(), manifest: daemon.NewManifest(nil, clock), store: storetest.NewMemStore(), clock: clock}
	r.fan = daemon.NewConductorFanOut(r.store, clock)
	if err := daemon.RegisterConductorExecuteHandler(r.reg, r.manifest, providerregistry.NewReader(reg),
		conductor.NewQuotaPolicy(cfg, clock), resolver, audit.New(storetest.NewMemStore(), clock, nil), clock,
		security, daemon.ConductorAccounting{}, r.fan); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConductorExecuteRedirectIsError(t *testing.T) {
	target := newRecordedProvider(t, false)
	var keyHeaders atomic.Int32
	inner := target.srv.Config.Handler
	target.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			keyHeaders.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	var redirects atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, target.srv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	r := redirectRig(t, redirector.URL)
	out, kind, msg := r.call(context.Background(), t, fanParams(1, "", "hello"))
	if out != nil || msg == "" {
		t.Fatalf("execute over a 302 = %v, want an error", out)
	}
	if kind != cascade.KindInternal.String() || !strings.Contains(msg, "302") {
		t.Fatalf("error = kind %q, message %q; want kind %q naming 302", kind, msg, cascade.KindInternal)
	}
	if redirects.Load() == 0 {
		t.Fatal("the redirecting base URL was never called")
	}
	if n := target.calls.Load(); n != 0 {
		t.Fatalf("redirect target received %d requests, want 0", n)
	}
	if n := keyHeaders.Load(); n != 0 {
		t.Fatalf("redirect target saw %d key headers, want 0", n)
	}

	// The same composition straight at the target completes.
	ok := redirectRig(t, target.srv.URL)
	out, kind, msg = ok.call(context.Background(), t, fanParams(1, "", "hello"))
	if out == nil {
		t.Fatalf("execute over a 200 failed: %s %s", kind, msg)
	}
	if target.calls.Load() == 0 {
		t.Fatal("direct execute never reached the provider")
	}
}
