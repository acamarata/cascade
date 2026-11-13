//go:build integration && !windows

package delegation

// Purpose: the delegate and delegate_wait round trip over a real daemon
// socket. An rpc.Handler with the access policy on serves both verbs on a
// unix socket under a t.TempDir CASCADE_HOME; the caller principal is minted
// by the daemon's principal.Minter and resolved from its header. The
// delegation service runs with a bound collection under a daemon.Manifest,
// and its spawner resolves the lane to a test-only driver registered through
// agents.RegisterHarness, so no vendor CLI runs. Only the same principal
// reads the addressed result back.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	socketHarness = "antigravity"
	socketBinary  = "agy"
)

var (
	_ = rpc.DeclareAccess("conductor.delegate", rpc.AccessOwnerView)
	_ = rpc.DeclareAccess("conductor.delegate_wait", rpc.AccessOwnerView)
	_ = agents.RegisterHarness(agents.Binding{Harness: socketHarness, PluginID: socketHarness,
		Runtime: agents.RuntimeHarnessCLI, LaneID: agents.LanePrefix + socketHarness, ConfigDirVar: "TEST_AGENT_CONFIG_DIR",
		Credentials: []agents.CredentialSpec{{Class: agents.CredentialSubscription}}, Binaries: []string{socketBinary}},
		func(agents.SpawnSite) (provider.AgentProvider, error) { return socketDriver{}, nil })
)

// socketDriver is the test-only harness driver: every job is accepted at once.
type socketDriver struct{ provider.AgentProvider }

func (socketDriver) Status(context.Context, provider.AgentJobID) (provider.AgentRunState, error) {
	return provider.AgentRunAccepted, nil
}

func (socketDriver) Collect(_ context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	return provider.CollectResult{JobID: id, Output: "round trip done", Artifacts: []string{"answer.md"},
		DataClass: provider.SensitivityInternal}, nil
}

// harnessShim resolves the leased lane through the registered harness table.
type harnessShim struct{ resolver *agents.Resolver }

func (h harnessShim) Spawn(_ context.Context, r agents.SpawnRequest) (agents.SpawnHandle, error) {
	_, factory, err := h.resolver.Resolve(r.Lane)
	if err != nil {
		return agents.SpawnHandle{}, err
	}
	drv, err := factory(agents.SpawnSite{})
	if err != nil {
		return agents.SpawnHandle{}, err
	}
	return agents.SpawnHandle{JobID: r.Job.JobID, Driver: drv, Release: func(context.Context) error { return nil }}, nil
}

// socketDaemon is the served daemon: store, minter, service, manifest and client.
type socketDaemon struct {
	chain    *chain
	minter   *principal.Minter
	svc      *Service
	manifest *daemon.Manifest
	notify   *notify.Service
	fleet    *fleetDouble
	client   *http.Client
}

func newSocketDaemon(t *testing.T) *socketDaemon {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, dir)
	}
	t.Chdir(dir) // a relative socket path stays under the sun_path limit
	resolver, err := agents.NewResolver(func(string) bool { return true })
	failIf(t, err != nil, "NewResolver: %v", err)
	clock := runtime.NewFixedClock(t0)
	d := &socketDaemon{chain: newChain(t), minter: principal.NewMinter(clock), manifest: daemon.NewManifest(nil, clock),
		notify: notify.NewService(notify.DefaultConfig(), clock, nil), fleet: &fleetDouble{lane: agents.LanePrefix + socketHarness}}
	d.svc = NewService(Deps{Clock: clock, Store: d.chain.st, Pinned: d.chain.s.PublicKey(), Signer: d.chain.s,
		Minter: d.minter, Shim: harnessShim{resolver: resolver}}.WithFleet(d.fleet))
	d.svc.SetAgentLanes([]capacity.AgentLane{{DriverID: socketHarness, Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
	err = d.svc.BindCollection(Collection{Ctx: context.Background(), Supervisor: d.manifest, Notify: d.notify, MaxWallClock: time.Hour,
		Ticks: func() runtime.Ticker { return &manualTicker{c: make(chan struct{})} }})
	failIf(t, err != nil, "BindCollection: %v", err)
	d.serve(t)
	return d
}

// serve mounts both verbs behind the access policy on a unix socket.
func (d *socketDaemon) serve(t *testing.T) {
	t.Helper()
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(tok string) (principal.Principal, error) { return d.minter.Resolve(tok, t0) })
	reg.Register("conductor.delegate", d.delegate)
	reg.Register("conductor.delegate_wait", d.wait)
	ln, err := net.Listen("unix", "d.sock")
	failIf(t, err != nil, "listen: %v", err)
	srv := &http.Server{Handler: rpc.NewHandler(reg), ConnContext: rpc.ConnContext, ReadHeaderTimeout: time.Minute}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-served; d.manifest.Wait() })
	d.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "d.sock")
	}}}
}

// delegate delegates from the calling principal's own stored record.
func (d *socketDaemon) delegate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "delegation: params")
	}
	c, _ := principal.CallerFrom(ctx)
	if c.Principal == nil {
		return nil, deniedf("the caller is not a principal")
	}
	parent, ok, err := d.chain.st.DelegationByAgentPrincipal(ctx, c.Principal.ID)
	if err != nil || !ok {
		return nil, cmp.Or[error](err, deniedf("the caller holds no delegation to delegate from"))
	}
	resp, err := d.svc.Delegate(ctx, &Request{AgentID: p.AgentID, Parent: parent})
	if err != nil {
		return nil, err
	}
	return map[string]string{"job_id": resp.Record.Env.ID}, nil
}

// wait is delegate_wait over the service's origin-checked Wait.
func (d *socketDaemon) wait(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "delegation: params")
	}
	return d.svc.Wait(ctx, p.JobID, 0)
}

// call posts one JSON-RPC request with the caller header hdr (an empty name sends none).
func (d *socketDaemon) call(t *testing.T, hdr [2]string, method string, params any) (json.RawMessage, *rpc.ErrorObject) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	failIf(t, err != nil, "encode: %v", err)
	req, err := http.NewRequest(http.MethodPost, "http://unix"+rpc.RPCPath, bytes.NewReader(body))
	failIf(t, err != nil, "request: %v", err)
	req.Header.Set("Content-Type", "application/json")
	if hdr[0] != "" {
		req.Header.Set(hdr[0], hdr[1])
	}
	resp, err := d.client.Do(req)
	failIf(t, err != nil, "%s: %v", method, err)
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *rpc.ErrorObject `json:"error"`
	}
	failIf(t, json.NewDecoder(resp.Body).Decode(&env) != nil, "%s: undecodable response (HTTP %d)", method, resp.StatusCode)
	return env.Result, env.Error
}

// mint returns the caller header for a minted session principal.
func (d *socketDaemon) mint(t *testing.T, id, scope string) [2]string {
	t.Helper()
	tok, err := d.minter.Mint(principal.Principal{ID: id, Kind: principal.KindSession, Scope: scope}, time.Hour)
	failIf(t, err != nil, "Mint %s: %v", id, err)
	return [2]string{principal.HeaderToken, tok}
}

// origin stores a root record owned by id, so id may delegate from it and wait on its children.
func (d *socketDaemon) origin(t *testing.T, id string) jobs.DelegationRecord {
	t.Helper()
	root, err := MintRoot(context.Background(), d.chain.s, principal.Principal{ID: id}, srcMut())
	failIf(t, err != nil, "MintRoot: %v", err)
	rec, err := d.chain.insert(d.chain.wire(root), root.Env, nil)
	failIf(t, err != nil || rec.AgentPrincipal != id || rec.OriginPrincipal != id, "store the root: %+v, %v", rec, err)
	return rec
}

// readResult asserts the origin's delegate_wait returns the collected result of id.
func (d *socketDaemon) readResult(t *testing.T, hdr [2]string, id string) {
	t.Helper()
	raw, eo := d.call(t, hdr, "conductor.delegate_wait", map[string]string{"job_id": id})
	var got WaitResult
	failIf(t, eo != nil || json.Unmarshal(raw, &got) != nil, "origin wait = %s, %+v", raw, eo)
	failIf(t, got.JobID != id || got.State != jobs.DelegationSucceeded || got.Output != "round trip done" ||
		len(got.Artifacts) != 1 || got.Artifacts[0] != "answer.md", "origin wait = %+v; want the collected result", got)
}

func TestDelegateRoundTrip(t *testing.T) {
	d := newSocketDaemon(t)
	d.origin(t, "rt-origin")
	sess, other := d.mint(t, "rt-origin", "project:rt"), d.mint(t, "rt-other", "project:other")
	_, eo := d.call(t, other, "conductor.delegate", map[string]string{"agent_id": "rt-intruder"})
	wantSocketDenied(t, "a principal with no record", eo, "holds no delegation")
	raw, eo := d.call(t, sess, "conductor.delegate", map[string]string{"agent_id": "rt-child"})
	var out struct {
		JobID string `json:"job_id"`
	}
	failIf(t, eo != nil || json.Unmarshal(raw, &out) != nil || out.JobID == "", "delegate = %s, %+v; want a job id", raw, eo)
	d.manifest.Wait()
	rec, err := d.chain.st.GetDelegation(context.Background(), out.JobID)
	failIf(t, err != nil || rec.State != jobs.DelegationSucceeded || rec.OriginPrincipal != "rt-origin" ||
		strings.Join(d.fleet.calls, ",") != "commit,release", "record = %+v, %v, fleet %v", rec, err, d.fleet.calls)
	d.notify.Dispatcher.Drain(context.Background())
	d.readResult(t, sess, out.JobID)
	for _, r := range []struct {
		who, want string
		hdr       [2]string
	}{
		{"another principal", "only the delegation origin may wait", other},
		{"the owner CLI", "only the delegation origin may wait", [2]string{principal.HeaderOwnerCLI, "owner-cli"}},
		{"an unknown token", "caller principal not recognized", [2]string{principal.HeaderToken, "fake-" + "token"}},
		{"no caller", "caller not permitted", [2]string{}},
	} {
		raw, eo := d.call(t, r.hdr, "conductor.delegate_wait", map[string]string{"job_id": out.JobID})
		failIf(t, len(raw) != 0 && string(raw) != "null", "%s read %s", r.who, raw)
		wantSocketDenied(t, r.who, eo, r.want)
	}
	d.readResult(t, sess, out.JobID) // the refusals neither leak nor consume the result
}

// wantSocketDenied asserts eo is a KindPermissionDenied error object naming want.
func wantSocketDenied(t *testing.T, who string, eo *rpc.ErrorObject, want string) {
	t.Helper()
	failIf(t, eo == nil || eo.Code != cascade.KindPermissionDenied.JSONRPCCode() || !strings.Contains(eo.Message, want),
		"%s: error = %+v; want KindPermissionDenied naming %q", who, eo, want)
}
