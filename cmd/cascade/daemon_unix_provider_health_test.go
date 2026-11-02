//go:build !windows

// Purpose: production reach of provider health (P1-GWY-09): the daemon's
// conductor wiring starts the scheduler on the run context and joins it, one
// Manager serves both the resolver's transient counter and the scheduler, and
// the production Executor stops retrying a lane whose provider was demoted
// mid-call and spills to another provider.
// Inputs: temp providers.db files, scripted transports, injected paths.
// Outputs: none.
// Constraints: no network, no vault, no keychain; HOME is a temp dir.
// SPORT: cmd/cascade daemon provider_health/ADD (P1-GWY-09).

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	"github.com/acamarata/cascade/internal/providers/health"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// hostScript answers by the first letter of the request host (p or q) and
// counts the sends per host. Host p fails with a 500, host q serves.
type hostScript struct {
	mu   sync.Mutex
	hits map[string]int
}

func (h *hostScript) Send(_ context.Context, _, url string, _ map[string]string, _ []byte) (int, map[string][]string, io.ReadCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits == nil {
		h.hits = map[string]int{}
	}
	host := strings.TrimPrefix(url, "https://")[:1]
	h.hits[host]++
	if host == "p" {
		return 500, nil, io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"failure"}}`)), nil
	}
	return 200, nil, io.NopCloser(strings.NewReader(gatewayTestOK)), nil
}

func (h *hostScript) count(host string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[host]
}

// keyCreds answers every key reference with a fixed fake value.
type keyCreds struct{}

func (keyCreds) Resolve(context.Context, string) (string, error) { return "cred-value", nil }

func seedHealthProvider(t *testing.T, reg *providerregistry.Registry, name string, status providerregistry.HealthStatus) {
	t.Helper()
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, providerregistry.ProviderRecord{Name: name, Driver: providerregistry.DriverAnthropic,
		BaseURL: "https://" + name + ".invalid", Auth: providerregistry.AuthKey, AuthRef: providerregistry.VaultKeyRef(name + "-key"),
		KnownModels: []string{"model-a"}, AccountKind: providerregistry.AccountPersonal, Tier: providerregistry.TierMid,
		HealthStatus: status}); err != nil {
		t.Fatalf("UpsertProvider %s: %v", name, err)
	}
	if err := reg.UpsertLane(ctx, providerregistry.LaneRecord{LaneName: "lane-" + name, ProviderName: name, Weight: 1,
		Capacity: providerregistry.CapacityAPICredit, State: providerregistry.LaneStateAvailable}); err != nil {
		t.Fatalf("UpsertLane %s: %v", name, err)
	}
}

func joinWithin(t *testing.T, m *daemon.Manifest, d time.Duration) {
	t.Helper()
	joined := make(chan struct{})
	go func() { m.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(d):
		t.Fatal("the manifest's goroutines were not joined after the run context ended")
	}
}

// assertWiringStartsAndJoinsScheduler checks the conductor wiring starts the scheduler and joins it.
func assertWiringStartsAndJoinsScheduler(t *testing.T, clock runtime.Clock) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	paths := fakeMemoryPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	// The conductor wiring itself starts the scheduler under the run context.
	run, cancel := context.WithCancel(context.Background())
	manifest := daemon.NewManifest(nil, clock)
	if _, err := wireConductorExecute(run, rpc.NewRegistry(), manifest, paths, clock, storetest.NewMemStore(), nil); err != nil {
		t.Fatalf("wireConductorExecute: %v", err)
	}
	if st, ok := subsystemState(manifest, providerHealthSubsystem); !ok || st.State != daemon.SubsystemRunning {
		t.Fatalf("subsystem %q = %+v (registered %v), want started by the conductor wiring", providerHealthSubsystem, st, ok)
	}
	cancel()
	joinWithin(t, manifest, 5*time.Second)
	if st, _ := subsystemState(manifest, providerHealthSubsystem); st.State == daemon.SubsystemRunning {
		t.Fatalf("subsystem still %+v after the run context ended", st)
	}
}

func TestDaemonComposesProviderHealth(t *testing.T) {
	resetProcessFirewall(t)
	clock := runtime.NewSystemClock()
	assertWiringStartsAndJoinsScheduler(t, clock)

	// One Manager: the scheduler recovers a degraded provider, and the same
	// Manager is what a resolver built WithHealth demotes through.
	reg, closeReg := openLaneRegistry(t, t.TempDir(), clock)
	t.Cleanup(closeReg)
	seedHealthProvider(t, reg, "p", providerregistry.HealthDegraded)
	seedHealthProvider(t, reg, "q", providerregistry.HealthHealthy)
	run2, cancel2 := context.WithCancel(context.Background())
	m2 := daemon.NewManifest(nil, clock)
	mgr, err := startProviderHealth(run2, m2, reg, events.New(storetest.NewMemStore(), clock), clock,
		providerDeps{HealthHTTPDoer: fakeHTTPDoer{status: 200}}, gatewayProbeConfig{})
	if err != nil {
		t.Fatalf("startProviderHealth: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for st, _ := mgr.GetHealth(context.Background(), "p"); st != provider.HealthHealthy; st, _ = mgr.GetHealth(context.Background(), "p") {
		if time.Now().After(deadline) {
			t.Fatalf("the scheduler never recovered the degraded provider (health %q)", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	resolver, err := providerdispatch.NewResolver(reg, keyCreds{}, clock, &hostScript{}, providerdispatch.WithHealth(mgr))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < providerdispatch.DegradeAfter; i++ {
		if err := callLane(t, resolver, "lane-p"); err == nil {
			t.Fatalf("lane-p call %d succeeded, want the scripted 500 to fail it", i+1)
		}
	}
	if st, _ := mgr.GetHealth(context.Background(), "p"); st != provider.HealthDegraded {
		t.Fatalf("p health = %q after %d transient outcomes through the shared Manager, want degraded", st, providerdispatch.DegradeAfter)
	}
	cancel2()
	joinWithin(t, m2, 5*time.Second)
}

// callLane runs one chat on lane through resolver and returns its error.
func callLane(t *testing.T, resolver *providerdispatch.Resolver, lane string) error {
	t.Helper()
	p, err := resolver.Resolve(context.Background(), provider.Selection{LaneID: lane, Model: "model-a"})
	if err != nil {
		t.Fatalf("Resolve %s: %v", lane, err)
	}
	_, cerr := p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	return cerr
}

// executeChatThroughConductor registers the conductor execute handler on resolver and runs one chat task.
func executeChatThroughConductor(t *testing.T, paths fakeMemoryPaths, clock runtime.Clock, reg *providerregistry.Registry,
	mgr *health.Manager, resolver *providerdispatch.Resolver, store *storetest.MemStore) {
	t.Helper()
	security, err := conductorSecurity(paths)
	if err != nil {
		t.Fatalf("conductorSecurity: %v", err)
	}
	policy, err := buildConductorPolicy(func() *runtime.Config { return &runtime.Config{} }, clock)
	if err != nil {
		t.Fatalf("buildConductorPolicy: %v", err)
	}
	security.Policy = conductor.NewOwnerPolicy(policy.Deny)
	rpcReg := rpc.NewRegistry()
	opts := conductorRouterOptionsEligible(nil, paths, clock, laneEligibleByHealth(reg, mgr), nil)
	if err := daemon.RegisterConductorExecuteHandler(rpcReg, daemon.NewManifest(nil, clock), providerregistry.NewReader(reg),
		policy.Quota, resolver, audit.New(store, clock, nil), clock, security, daemon.ConductorAccounting{},
		daemon.NewConductorFanOut(store, clock), opts...); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: %v", err)
	}
	params, _ := json.Marshal(map[string]any{"task_id": "t1", "task_class": "chat", "sensitivity": "public",
		"inputs": []map[string]string{{"role": "user", "content": "hello"}}})
	if _, errObj := rpcReg.Dispatch(context.Background(), &rpc.Request{Method: daemon.ConductorExecuteMethod, Params: params}); errObj != nil {
		t.Fatalf("execute failed: %s", errObj.Message)
	}
}

func TestRetryStopsWhenLaneDemoted(t *testing.T) {
	resetProcessFirewall(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	paths := fakeMemoryPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewSystemClock()
	reg, closeReg := openLaneRegistry(t, paths.DataDir(), clock)
	t.Cleanup(closeReg)
	seedHealthProvider(t, reg, "p", providerregistry.HealthUnknown)
	seedHealthProvider(t, reg, "q", providerregistry.HealthUnknown)
	store := storetest.NewMemStore()
	mgr := health.NewManager(reg, events.New(store, clock), clock, 0)
	tr := &hostScript{}
	resolver, err := providerdispatch.NewResolver(reg, keyCreds{}, clock, tr, providerdispatch.WithHealth(mgr))
	if err != nil {
		t.Fatal(err)
	}
	// Two transient outcomes on p from earlier calls: the next one demotes.
	for i := 0; i < providerdispatch.DegradeAfter-1; i++ {
		if _, ok := provider.TransientOf(callLane(t, resolver, "lane-p")); !ok {
			t.Fatal("priming call was not a transient failure")
		}
	}
	primed := tr.count("p")

	executeChatThroughConductor(t, paths, clock, reg, mgr, resolver, store)
	if got := tr.count("p") - primed; got != 1 {
		t.Fatalf("p was sent %d times in the call, want exactly 1 (demoted by it, never retried)", got)
	}
	if tr.count("q") != 1 {
		t.Fatalf("q was sent %d times, want 1 (the spill)", tr.count("q"))
	}
	if st, _ := mgr.GetHealth(context.Background(), "p"); st != provider.HealthDegraded {
		t.Fatalf("p health = %q, want degraded", st)
	}
}
