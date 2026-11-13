//go:build !windows

// Purpose: the delegate wiring's tests: the registration mounts both verbs once
// and holds the start-time contract, an owner round trip runs through the wired
// service (fake fleet and driver, real store, collector and notify inbox), and
// every refusal is a typed cascade error.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/delegation"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"

	_ "modernc.org/sqlite" // the "sqlite" database/sql driver for the real store
)

var delegateT0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type delegateSigner struct{ priv ed25519.PrivateKey }

func (s delegateSigner) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }
func (s delegateSigner) Sign(_ context.Context, m []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, m), nil
}

// delegateFleet leases agent:drv-1 and logs every call.
type delegateFleet struct {
	mu    sync.Mutex
	calls []string
}

func (f *delegateFleet) log(c string) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	return nil
}
func (f *delegateFleet) Lease(context.Context, capacity.ResourceRequest, capacity.DispatchRef, int) (capacity.Lease, error) {
	return capacity.Lease{ID: "lease-1", LaneID: "agent:drv-1"}, f.log("lease")
}
func (f *delegateFleet) Commit(context.Context, capacity.Lease) error  { return f.log("commit") }
func (f *delegateFleet) Abort(context.Context, capacity.Lease) error   { return f.log("abort") }
func (f *delegateFleet) Release(context.Context, capacity.Lease) error { return f.log("release") }

// delegateDriver is accepted at once and collects "finished".
type delegateDriver struct{ provider.AgentProvider }

func (delegateDriver) Status(context.Context, provider.AgentJobID) (provider.AgentRunState, error) {
	return provider.AgentRunAccepted, nil
}
func (delegateDriver) Collect(_ context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	return provider.CollectResult{JobID: id, Output: "finished", DataClass: provider.SensitivityInternal}, nil
}

// delegateShim records how many spawns were live when it ran and counts releases.
type delegateShim struct {
	live           *agents.SpawnRegistry
	seen, releases int
	err            error
}

func (d *delegateShim) Spawn(_ context.Context, r agents.SpawnRequest) (agents.SpawnHandle, error) {
	d.seen = len(d.live.Live())
	if d.err != nil {
		return agents.SpawnHandle{}, d.err
	}
	return agents.SpawnHandle{JobID: r.Job.JobID, PGID: 42, Driver: delegateDriver{},
		Release: func(context.Context) error { d.releases++; return nil }}, nil
}

type delegateFixture struct {
	rt       *delegateRuntime
	reg      *rpc.Registry
	fleet    *delegateFleet
	shim     *delegateShim
	live     *agents.SpawnRegistry
	manifest *daemon.Manifest
	notify   *notify.Service
}

func newDelegateFixture(t *testing.T) *delegateFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	clock := runtime.NewFixedClock(delegateT0)
	if err := jobs.ApplyDelegationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, live := delegateSigner{priv: priv}, agents.NewSpawnRegistry()
	f := &delegateFixture{reg: rpc.NewRegistry(), fleet: &delegateFleet{}, shim: &delegateShim{live: live}, live: live,
		manifest: daemon.NewManifest(nil, clock), notify: notify.NewService(notify.DefaultConfig(), clock, nil)}
	svc := delegation.NewService(delegation.Deps{Clock: clock, Store: jobs.NewStore(db), Pinned: signer.PublicKey(), Signer: signer,
		Minter: principal.NewMinter(clock), Shim: registeringShim{spawn: f.shim, live: live}}.WithFleet(f.fleet))
	svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1", Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
	err = svc.BindCollection(delegation.Collection{Ctx: context.Background(), Supervisor: f.manifest, Notify: f.notify,
		MaxWallClock: time.Hour, Ticks: func() runtime.Ticker { return runtime.NewSystemTicker(time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	f.rt = &delegateRuntime{store: jobs.NewStore(db), clock: clock, base: t.TempDir(), wallClock: time.Hour,
		build: func(context.Context) (*delegation.Service, scopeenvelope.Signer, error) { return svc, signer, nil }}
	mountDelegate(f.reg, f.rt)
	return f
}

var delegateOwner = func() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func TestDelegateRoundTripThroughWiredService(t *testing.T) {
	f := newDelegateFixture(t)
	res, rpcErr := f.reg.Dispatch(delegateOwner(), &rpc.Request{Method: delegateMethod, Params: json.RawMessage(`{"intent":"summarise the notes"}`)})
	got, ok := res.(delegateResult)
	if rpcErr != nil || !ok || got.Status != "dispatched" || got.JobID == "" {
		t.Fatalf("delegate = %#v, %v; want a dispatched job", res, rpcErr)
	}
	f.manifest.Wait()
	f.notify.Dispatcher.Drain(context.Background())
	if f.shim.seen != 1 || f.shim.releases != 1 || len(f.live.Live()) != 0 {
		t.Fatalf("live at spawn %d, releases %d, live after %d; want 1, 1, 0", f.shim.seen, f.shim.releases, len(f.live.Live()))
	}
	if calls := strings.Join(f.fleet.calls, ","); calls != "lease,commit,release" {
		t.Fatalf("fleet calls %q; want lease,commit,release", calls)
	}
	params, err := json.Marshal(delegateWaitParams{JobID: got.JobID, TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	waited, rpcErr := f.reg.Dispatch(delegateOwner(), &rpc.Request{Method: delegateWaitMethod, Params: params})
	w, ok := waited.(delegation.WaitResult)
	if rpcErr != nil || !ok || w.Output != "finished" || w.State != jobs.DelegationSucceeded || w.JobID != got.JobID {
		t.Fatalf("delegate_wait = %#v, %v; want the finished result", waited, rpcErr)
	}
	if left, err := os.ReadDir(f.rt.base); err != nil || len(left) != 0 {
		t.Fatalf("worktrees left %v, %v; the collector releases the owner's", left, err)
	}
}

func TestDelegateRefusals(t *testing.T) {
	f := newDelegateFixture(t)
	h := delegateHandlers{rt: f.rt}
	if _, err := h.delegate(delegateOwner(), json.RawMessage(`{"intent":"x","mutation":true,"job_id":"j1"}`)); err != errDelegateMutationUnbound || err.Error() != errDelegateMutationUnbound.Error() {
		t.Fatalf("mutation = %v; want errDelegateMutationUnbound", err)
	}
	stranger := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "stranger"}})
	for _, c := range []struct {
		ctx  context.Context
		raw  string
		kind cascade.Kind
		msg  string
	}{
		{delegateOwner(), `{"intent":"x","bogus":1}`, cascade.KindInvalidInput, "params"},
		{delegateOwner(), `{"intent":""}`, cascade.KindInvalidInput, "intent is required"},
		{delegateOwner(), `{"intent":"x"} {}`, cascade.KindInvalidInput, "trailing data"},
		{stranger, `{"intent":"x"}`, cascade.KindPermissionDenied, "holds no delegation"},
		{context.Background(), `{"intent":"x"}`, cascade.KindPermissionDenied, "neither a principal nor the owner CLI"},
	} {
		_, err := h.delegate(c.ctx, json.RawMessage(c.raw))
		if !cascade.HasKind(err, c.kind) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: err = %v; want %v naming %q", c.raw, err, c.kind, c.msg)
		}
	}
	if len(f.fleet.calls) != 0 {
		t.Fatalf("a refused delegate reached the fleet: %v", f.fleet.calls)
	}
	unwired := delegateHandlers{rt: &delegateRuntime{reason: "no fleet runtime or config"}}
	if _, err := unwired.wait(delegateOwner(), json.RawMessage(`{"job_id":"j","timeout_ms":1}`)); !cascade.HasKind(err, cascade.KindUnavailable) ||
		!strings.Contains(err.Error(), "no fleet runtime or config") {
		t.Fatalf("unwired wait = %v; want KindUnavailable naming the reason", err)
	}
}

func TestDelegateRegistrationWiresOnce(t *testing.T) {
	var found []daemonRegistration
	for _, r := range daemonRegistrations {
		if r.Name == "delegate" {
			found = append(found, r)
		}
	}
	if len(found) != 1 || found[0].Phase != phaseConductor || found[0].Order != 50 {
		t.Fatalf("delegate registrations = %+v; want one at phaseConductor order 50", found)
	}
	reg := rpc.NewRegistry()
	if err := found[0].Wire(&daemonWiring{Ctx: context.Background(), Registry: reg, Clock: runtime.NewFixedClock(delegateT0)}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range reg.Methods() {
		if m == delegateMethod || m == delegateWaitMethod {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("the registration mounted %d delegate verbs; want 2", n)
	}
	calls := delegateWireCalls(t)
	for _, bad := range []string{"SetOwnerPeerGuard", "HideDaemonEnviron"} {
		if calls[bad] {
			t.Errorf("wire_delegate.go calls %s; the RPC server arms the guard", bad)
		}
	}
	for _, need := range []string{"RecoverPendingSpawns", "NewService", "NewShim", "NewResolver", "MintRoot", "MintChild", "AllocWorktree", "SetAgentLanes"} {
		if !calls[need] {
			t.Errorf("wire_delegate.go never calls %s", need)
		}
	}
	if !calls["RepoIDOf"] {
		t.Error("wire_delegate.go never binds RepoIDOf")
	}
}

// delegateWireCalls names every function wire_delegate.go calls or selects.
func delegateWireCalls(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "wire_delegate.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			out[x.Sel.Name] = true
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

func TestDelegateStartHelpers(t *testing.T) {
	lanes := delegateLanes(runtime.AgentsConfig{Lanes: []runtime.AgentLaneConfig{
		{Driver: "a", Enabled: true, Posture: runtime.AgentPostureProgrammatic}, {Driver: "b"}}})
	if len(lanes) != 1 || lanes[0].DriverID != "a" || !lanes[0].Posture.ProgrammaticEntitlement {
		t.Fatalf("lanes = %+v; want only the enabled programmatic lane", lanes)
	}
	if _, _, err := resolveRecoveredLease(context.Background(), jobs.DelegationRecord{ID: "d", LeaseID: "l"}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("leased recovery = %v; want KindUnavailable", err)
	}
	if l, committed, err := resolveRecoveredLease(context.Background(), jobs.DelegationRecord{ID: "d"}); err != nil || l.ID != "" || committed {
		t.Fatalf("unleased recovery = %+v, %v, %v; want nothing to release", l, committed, err)
	}
	rt := &delegateRuntime{base: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(rt.base, "ro-abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"agent-x", ownerHopPrefix + "../x", ownerHopPrefix + "abc"} {
		if err := rt.releaseOwnerWorktree(context.Background(), jobs.DelegationRecord{AgentPrincipal: p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(rt.base, "ro-abc")); !os.IsNotExist(err) {
		t.Fatalf("the owner hop worktree remains: %v", err)
	}
	live := agents.NewSpawnRegistry()
	boom := cascade.New(cascade.KindUnavailable, "spawn refused")
	if _, err := (registeringShim{spawn: &delegateShim{live: live, err: boom}, live: live}).Spawn(context.Background(), agents.SpawnRequest{}); err != boom || len(live.Live()) != 0 {
		t.Fatalf("refused spawn = %v, live %d; want boom and none live", err, len(live.Live()))
	}
}
