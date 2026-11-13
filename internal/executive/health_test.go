package executive

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
}

func pct(v float64) *float64 { return &v }

func ac7Source() map[economics.HealthKey]economics.KeyHealth {
	return map[economics.HealthKey]economics.KeyHealth{
		economics.HealthKeyExecutive: {State: economics.HealthStateProtected, Percentage: pct(0.12)},
		economics.HealthKeyBuild:     {State: economics.HealthStateHealthy, Percentage: pct(0.9)},
		economics.HealthKeyBulk:      {State: economics.HealthStateScarce},
		economics.HealthKeyAdversary: {State: economics.HealthStateAbundant, Percentage: pct(0.75)},
	}
}

func TestEXE14LegacyAC7(t *testing.T) {
	isolateHome(t)
	src := ac7Source()
	got, err := BuildHealthSummary(src)
	if err != nil {
		t.Fatalf("BuildHealthSummary: %v", err)
	}
	want := []struct {
		key   economics.HealthKey
		state economics.HealthState
		pct   *float64
	}{
		{economics.HealthKeyExecutive, economics.HealthStateProtected, pct(0.12)},
		{economics.HealthKeyBuild, economics.HealthStateHealthy, pct(0.9)},
		{economics.HealthKeyBulk, economics.HealthStateScarce, nil},
		{economics.HealthKeyAdversary, economics.HealthStateAbundant, pct(0.75)},
		{economics.HealthKeyLongContext, economics.HealthStateUnavailable, nil},
	}
	for i, w := range want {
		k := got.Keys[i]
		if k.Key != w.key || k.State != w.state {
			t.Fatalf("key %d = %s/%s, want %s/%s", i, k.Key, k.State, w.key, w.state)
		}
		if (k.Percentage == nil) != (w.pct == nil) || (w.pct != nil && *k.Percentage != *w.pct) {
			t.Fatalf("key %s percentage = %v, want %v", k.Key, k.Percentage, w.pct)
		}
	}
	*src[economics.HealthKeyExecutive].Percentage = 0.99
	if *got.Keys[0].Percentage != 0.12 {
		t.Fatalf("summary aliased the source percentage: %v", *got.Keys[0].Percentage)
	}
}

func TestHealthSummaryRefusesUnknownKeyOrState(t *testing.T) {
	isolateHome(t)
	badKey := ac7Source()
	badKey["gpu"] = economics.KeyHealth{State: economics.HealthStateHealthy}
	badState := ac7Source()
	badState[economics.HealthKeyBuild] = economics.KeyHealth{State: "ok"}
	for name, src := range map[string]map[economics.HealthKey]economics.KeyHealth{"key": badKey, "state": badState} {
		got, err := BuildHealthSummary(src)
		if !errors.Is(err, ErrHealthSource) {
			t.Fatalf("%s: err = %v, want ErrHealthSource", name, err)
		}
		if err.Error() != ErrHealthSource.Error() {
			t.Fatalf("%s: refusal contains source metadata", name)
		}
		if got != (HealthSummary{}) {
			t.Fatalf("%s: summary = %+v, want zero", name, got)
		}
	}
	got, err := BuildHealthSummary(nil)
	if err != nil {
		t.Fatalf("nil map: %v", err)
	}
	for _, k := range got.Keys {
		if k.State != economics.HealthStateUnavailable || k.Percentage != nil || !k.Key.Valid() {
			t.Fatalf("nil map key = %+v, want unavailable", k)
		}
	}
}

func TestSummarizeFleetHealthCallsHealthOnce(t *testing.T) {
	isolateHome(t)
	calls := 0
	ok := func(context.Context) (map[economics.HealthKey]economics.KeyHealth, error) {
		calls++
		return ac7Source(), nil
	}
	got, err := SummarizeFleetHealth(context.Background(), ok)
	if err != nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d, want nil and 1", err, calls)
	}
	if got.Keys[1].State != economics.HealthStateHealthy {
		t.Fatalf("build = %+v", got.Keys[1])
	}

	injected := errors.New("read failed")
	calls = 0
	fail := func(context.Context) (map[economics.HealthKey]economics.KeyHealth, error) {
		calls++
		return nil, injected
	}
	got, err = SummarizeFleetHealth(context.Background(), fail)
	if calls != 1 || got != (HealthSummary{}) || !errors.Is(err, injected) {
		t.Fatalf("calls = %d, summary = %+v, err = %v", calls, got, err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v, want KindUnavailable", kind)
	}
	for _, k := range got.Keys {
		if k.State == economics.HealthStateHealthy {
			t.Fatalf("error path reported a healthy key")
		}
	}

	_, err = SummarizeFleetHealth(context.Background(), nil)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Fatalf("nil health: kind = %v, err = %v, want KindInvalidInput", kind, err)
	}
}

func TestHealthSummaryJSONShape(t *testing.T) {
	isolateHome(t)
	got, err := BuildHealthSummary(ac7Source())
	if err != nil {
		t.Fatalf("BuildHealthSummary: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"keys":[{"key":"executive","state":"protected","percentage":0.12},{"key":"build","state":"healthy","percentage":0.9},{"key":"bulk","state":"scarce"},{"key":"adversary","state":"abundant","percentage":0.75},{"key":"long_context","state":"unavailable"}]}`
	if string(b) != want {
		t.Fatalf("json =\n%s\nwant\n%s", b, want)
	}
}

// newHealthRuntime builds the named producer with isolated storage and clock.
func newHealthRuntime(t *testing.T) *daemon.FleetRuntime {
	t.Helper()
	isolateHome(t)
	home := t.TempDir()
	paths, err := runtime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	store := storetest.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	manifest := daemon.NewManifest(nil, clock)
	fleet, err := daemon.NewFleetRuntime(ctx, daemon.FleetRuntimeDeps{
		Paths: paths, Clock: clock, Manifest: manifest, Bus: events.New(store, clock),
		Store: store, Config: &runtime.Config{}, Binding: nodes.NewFileControllerBindingBackend(paths.DataDir()),
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = fleet.Close(); manifest.Wait() })
	seedHealthTopology(ctx, t, fleet, clock)
	return fleet
}

func seedHealthTopology(ctx context.Context, t *testing.T, fleet *daemon.FleetRuntime, clock runtime.Clock) {
	t.Helper()
	reg := fleet.CapacityParts().Registry
	if err := reg.UpsertProvider(ctx, registry.ProviderRecord{Name: "fixture", Driver: registry.DriverOpenAICompat,
		Auth: registry.AuthKey, AuthRef: "fixture-reference", AccountKind: registry.AccountPersonal,
		Tier: registry.TierMid, BaseURL: "https://api.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.UpsertLane(ctx, registry.LaneRecord{LaneName: "fixture-lane", ProviderName: "fixture",
		Capacity: registry.CapacityInteractiveUsage, State: registry.LaneStateAvailable}); err != nil {
		t.Fatal(err)
	}
	if _, err := topology.Reconcile(ctx, fleet.Topology(), reg, clock); err != nil {
		t.Fatal(err)
	}
	lanes, err := fleet.Topology().ListLanes(ctx, false)
	if err != nil || len(lanes) != 1 {
		t.Fatalf("fixture lanes: %v, %v", lanes, err)
	}
	lanes[0].LaneClass, lanes[0].Health = topology.LaneClassLead, topology.LaneHealthAvailable
	lanes[0].ID = "fixture-build"
	if err := fleet.Topology().UpsertLanes(ctx, lanes); err != nil {
		t.Fatal(err)
	}
}

// healthRPCAdapter copies the projection into the RPC-owned DTO, as the
// composition root does, without deriving health or reading another store.
func healthRPCAdapter(health HealthFunc) rpc.FleetHealthDeps {
	return rpc.FleetHealthDeps{Summary: func(ctx context.Context) (rpc.FleetHealthSummaryDTO, error) {
		summary, err := SummarizeFleetHealth(ctx, health)
		if err != nil {
			return rpc.FleetHealthSummaryDTO{}, err
		}
		var out rpc.FleetHealthSummaryDTO
		for i, k := range summary.Keys {
			out.Keys[i] = rpc.FleetHealthKeyDTO{Key: string(k.Key), State: string(k.State), Percentage: k.Percentage}
		}
		return out, nil
	}}
}

func TestHealthProductionHandoff(t *testing.T) {
	fleet := newHealthRuntime(t)
	ctx := context.Background()
	source, err := fleet.Health(ctx)
	if err != nil || len(source) != 5 || source[economics.HealthKeyBuild].State != economics.HealthStateAvailable {
		t.Fatalf("nonempty producer fixture: %v, %v", source, err)
	}
	want, err := BuildHealthSummary(source)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	deps := healthRPCAdapter(func(ctx context.Context) (map[economics.HealthKey]economics.KeyHealth, error) {
		calls++
		return fleet.Health(ctx)
	})
	r := rpc.NewRegistry()
	if err := rpc.RegisterFleetHealthSummary(r, deps); err != nil {
		t.Fatal(err)
	}
	result, refusal := r.Dispatch(ctx, &rpc.Request{Method: rpc.FleetHealthSummaryMethod})
	if refusal != nil || calls != 1 {
		t.Fatalf("positive dispatch: %v, calls %d", refusal, calls)
	}
	got := result.(rpc.FleetHealthSummaryDTO)
	for i, k := range want.Keys {
		if got.Status != "ok" || got.Keys[i].Key != string(k.Key) || got.Keys[i].State != string(k.State) || !reflect.DeepEqual(got.Keys[i].Percentage, k.Percentage) {
			t.Fatalf("projection mismatch at key %d", i)
		}
	}
	if _, err := fleet.JobsDB().ExecContext(ctx, `ALTER TABLE config_lane RENAME TO config_lane_hidden`); err != nil {
		t.Fatal(err)
	}
	result, refusal = r.Dispatch(ctx, &rpc.Request{Method: rpc.FleetHealthSummaryMethod})
	if calls != 2 || result != nil || refusal == nil || refusal.Code != cascade.KindUnavailable.JSONRPCCode() {
		t.Fatalf("read failure: result %v, refusal %v, calls %d", result, refusal, calls)
	}
	if _, err := fleet.JobsDB().ExecContext(ctx, `ALTER TABLE config_lane_hidden RENAME TO config_lane`); err != nil {
		t.Fatal(err)
	}
}
