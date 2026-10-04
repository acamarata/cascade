//go:build !windows && integration

// Purpose: the durable conductor.execute fan-out (contract:fanout-producer)
//
//	driven through the real handler, the real providers registry, the real
//	dispatch Resolver, the real Ollama driver and the real HTTP transport,
//	against a local recorded-provider server (testdata/recorded-provider,
//	provenance there). Only the credential source is a test double (it
//	hands the driver a placeholder bearer value for the local server).
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-19).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	providertransport "github.com/acamarata/cascade/providers/transport"
)

// recordedProvider replays the recorded /api/chat body, counting calls and
// the highest number in flight at once. A request is in flight from handler
// entry until its handler returns or its r.Context() is done, whichever is
// first. A blocking provider holds every call until release (or the
// request's own cancellation).
type recordedProvider struct {
	srv                   *httptest.Server
	calls, inflight, peak atomic.Int32
	ctxDone               atomic.Int32 // blocked requests ended by their own r.Context()
	free                  atomic.Int32 // calls answered at once before blocking starts
	entered               chan struct{}
	block                 chan struct{}
	once                  sync.Once
}

// enter counts one request in flight and returns the handler's exit hook;
// the count drops once, at the handler's return or when ctx is done.
func (p *recordedProvider) enter(ctx context.Context) func() {
	n := p.inflight.Add(1)
	for m := p.peak.Load(); n > m && !p.peak.CompareAndSwap(m, n); {
		m = p.peak.Load()
	}
	var once sync.Once
	leave := func() { once.Do(func() { p.inflight.Add(-1) }) }
	stop := context.AfterFunc(ctx, leave)
	return func() { stop(); leave() }
}

func newRecordedProvider(t *testing.T, block bool) *recordedProvider {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "recorded-provider", "ollama-chat.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	p := &recordedProvider{entered: make(chan struct{}, 64)}
	if block {
		p.block = make(chan struct{})
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body first: an HTTP/1.1 server watches the connection for
		// a client close (which cancels r.Context()) only after the request
		// body is consumed, so an unread body hides every client cancel.
		_, _ = io.Copy(io.Discard, r.Body)
		p.calls.Add(1)
		defer p.enter(r.Context())()
		p.entered <- struct{}{}
		if p.block != nil && p.free.Add(-1) < 0 {
			select {
			case <-p.block:
			case <-r.Context().Done():
				p.ctxDone.Add(1)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(func() { p.release(); p.srv.Close() })
	return p
}

// release unblocks every held and future call.
func (p *recordedProvider) release() {
	p.once.Do(func() {
		if p.block != nil {
			close(p.block)
		}
	})
}

// placeholderCreds answers the driver's bearer lookup for the local server.
type placeholderCreds struct{}

func (placeholderCreds) Resolve(context.Context, string) (string, error) {
	return "local-" + "placeholder", nil
}

// countingPolicy allows every request and counts each AuthorizeFn call.
type countingPolicy struct{ n atomic.Int32 }

func (p *countingPolicy) Authorize(context.Context, provider.ModelRequest) error {
	p.n.Add(1)
	return nil
}

// ctxStore deletes only while ctx is live, as a real store does.
type ctxStore struct{ provider.Store }

func (s ctxStore) Delete(ctx context.Context, ns, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Delete(ctx, ns, key)
}

// fanRig is one registered conductor.execute over a store.
type fanRig struct {
	reg      *rpc.Registry
	manifest *daemon.Manifest
	store    provider.Store
	fan      daemon.ConductorFanOut
	prov     *recordedProvider
	clock    runtime.Clock
}

// rigOpts tunes a rig; the zero value is the production shape.
type rigOpts struct {
	store  provider.Store
	block  bool
	policy conductor.PolicyEvaluator
	edit   func(*daemon.ConductorFanOut)
}

func newFanRig(t *testing.T, o rigOpts) *fanRig {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	paths := fakeMemoryPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &fanRig{reg: rpc.NewRegistry(), manifest: daemon.NewManifest(nil, clock), store: o.store, clock: clock,
		prov: newRecordedProvider(t, o.block)}
	if r.store == nil {
		r.store = storetest.NewMemStore()
	}
	reg := seedRecordedRegistry(ctx, t, paths, clock, r.prov.srv.URL)
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
	if o.policy != nil {
		security.Policy = o.policy
	}
	r.fan = daemon.NewConductorFanOut(r.store, clock)
	if o.edit != nil {
		o.edit(&r.fan)
	}
	if err := daemon.RegisterConductorExecuteHandler(r.reg, r.manifest, providerregistry.NewReader(reg),
		conductor.NewQuotaPolicy(cfg, clock), resolver, audit.New(storetest.NewMemStore(), clock, nil), clock,
		security, daemon.ConductorAccounting{}, r.fan); err != nil {
		t.Fatal(err)
	}
	return r
}

// seedRecordedRegistry opens the providers registry the way the
// composition root does and registers the recorded server as one lane.
func seedRecordedRegistry(ctx context.Context, t *testing.T, paths runtime.PathProvider, clock runtime.Clock, url string) *providerregistry.Registry {
	t.Helper()
	db, err := openMigratedDB(ctx, filepath.Join(paths.DataDir(), providerRegistryDBFile), func(ctx context.Context, db *sql.DB) error {
		return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := providerregistry.NewRegistry(db, clock)
	if err := reg.AddProvider(ctx, providerregistry.ProviderRecord{Name: "rec", Driver: providerregistry.DriverOllama,
		BaseURL: url, Auth: providerregistry.AuthKey, AuthRef: "recorded-provider-bearer", KnownModels: []string{"qwen2.5:0.5b"},
		AccountKind: providerregistry.AccountPersonal, Tier: providerregistry.TierFree, HealthStatus: providerregistry.HealthHealthy}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := reg.UpsertLane(ctx, providerregistry.LaneRecord{LaneName: "rec", ProviderName: "rec", Weight: 1, Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	return reg
}

// fanParams is a conductor.execute fan-out call.
func fanParams(n int, requestID, prompt string) map[string]any {
	p := map[string]any{"task_id": "cli-chat", "task_class": "chat", "sensitivity": "public", "fan_out": n,
		"inputs": []map[string]string{{"role": "user", "content": prompt}}}
	if requestID != "" {
		p["request_id"] = requestID
	}
	return p
}

// call dispatches one conductor.execute through the real registry and
// returns the decoded result, or the error's kind and message.
func (r *fanRig) call(ctx context.Context, t *testing.T, params map[string]any) (map[string]any, string, string) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, errObj := r.reg.Dispatch(ctx, &rpc.Request{Method: daemon.ConductorExecuteMethod, Params: raw})
	if errObj != nil {
		kind, _, _ := strings.Cut(errObj.Message, ":") // the message leads with the taxonomy kind
		return nil, kind, errObj.Message
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, "", ""
}

// counts reports the stored leg results and request records.
func (r *fanRig) counts(t *testing.T) (legs, requests int) {
	t.Helper()
	return countKeys(t, r.store, "conductor.fanout.legs"), countKeys(t, r.store, "conductor.fanout.requests")
}

func countKeys(t *testing.T, store provider.Store, ns string) int {
	t.Helper()
	it, err := store.Scan(context.Background(), ns, "")
	if err != nil {
		t.Fatalf("scan %s: %v", ns, err)
	}
	defer func() { _ = it.Close() }()
	n := 0
	for it.Next(context.Background()) {
		n++
	}
	return n
}

// entries replays one fan-out entity.
func (r *fanRig) entries(t *testing.T, id string) []journal.Entry {
	t.Helper()
	js := journal.New(r.store, r.clock, journal.DefaultNamespace)
	e, err := js.Replay(context.Background(), resume.FanOutEntity(id), journal.Cursor{EntityID: resume.FanOutEntity(id)}, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return e
}

// kindCount counts entries of kind k.
func kindCount(es []journal.Entry, k journal.Kind) int {
	n := 0
	for _, e := range es {
		if e.Kind == k {
			n++
		}
	}
	return n
}
