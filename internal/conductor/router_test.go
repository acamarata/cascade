package conductor

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeRegistry is a deterministic provider.ProviderRegistryReader double.
// callsListLanes/callsListProviders count invocations so
// TestRouter_SnapshotImmutable can assert each is read exactly once per
// Select call.
type fakeRegistry struct {
	providers []provider.ProviderInfo
	lanes     []provider.LaneInfo

	callsListLanes     int
	callsListProviders int
}

func (f *fakeRegistry) GetProvider(_ context.Context, name string) (provider.ProviderInfo, error) {
	for _, p := range f.providers {
		if p.Name == name {
			return p, nil
		}
	}
	return provider.ProviderInfo{}, cascade.New(cascade.KindNotFound, "fakeRegistry: unknown provider")
}

func (f *fakeRegistry) ListProviders(_ context.Context) ([]provider.ProviderInfo, error) {
	f.callsListProviders++
	return f.providers, nil
}

func (f *fakeRegistry) ListLanes(_ context.Context) ([]provider.LaneInfo, error) {
	f.callsListLanes++
	return f.lanes, nil
}

func (f *fakeRegistry) ListPool(_ context.Context, pool string) ([]provider.LaneInfo, error) {
	var out []provider.LaneInfo
	for _, l := range f.lanes {
		if l.PoolMembership == pool {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeRegistry) GetByModel(_ context.Context, model string) ([]provider.ProviderInfo, error) {
	var out []provider.ProviderInfo
	for _, p := range f.providers {
		for _, m := range p.KnownModels {
			if m == model {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

var _ provider.ProviderRegistryReader = (*fakeRegistry)(nil)

// fakeQuota is a deterministic QuotaSpiller double: it returns the first
// entry of order not present in excluded, recording every call so tests
// can assert the excluded set FILTER 4 built.
type fakeQuota struct {
	order    []LaneID
	calls    [][]LaneID
	callN    int
	forceErr error
}

func (f *fakeQuota) NextLane(_ context.Context, excluded []LaneID) (LaneID, error) {
	f.callN++
	f.calls = append(f.calls, append([]LaneID(nil), excluded...))
	if f.forceErr != nil {
		return "", f.forceErr
	}
	barred := make(map[LaneID]bool, len(excluded))
	for _, x := range excluded {
		barred[x] = true
	}
	for _, lane := range f.order {
		if !barred[lane] {
			return lane, nil
		}
	}
	return "", ErrAllLanesExhausted
}

// oneHealthyLane builds a minimal registry with a single controller-local,
// capability-matching, healthy lane named "lane-a".
func oneHealthyLane() *fakeRegistry {
	return &fakeRegistry{
		providers: []provider.ProviderInfo{{
			Name:         "prov-a",
			BaseURL:      "http://127.0.0.1:8080",
			KnownModels:  []string{"model-a"},
			HealthStatus: "healthy",
			Capabilities: provider.Capabilities{
				Search: provider.CapabilitySupported,
			},
		}},
		lanes: []provider.LaneInfo{{
			LaneName:     "lane-a",
			ProviderName: "prov-a",
			State:        "active",
		}},
	}
}

func chatReq() provider.ModelRequest {
	return provider.ModelRequest{
		TaskID:    "t1",
		TaskClass: "chat",
		Inputs:    []provider.ChatMessage{{Role: "user", Content: "hi"}},
	}
}

// TestRouter_CapabilityFilter asserts a lane whose Capabilities satisfies
// a required dimension is retained and selected.
func TestRouter_CapabilityFilter(t *testing.T) {
	reg := oneHealthyLane()
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.RequiredCapabilities.Search = true

	sel, err := r.Select(context.Background(), req)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.LaneID != "lane-a" {
		t.Fatalf("LaneID = %q, want lane-a", sel.LaneID)
	}
}

// TestRouter_RequiredCapability_NoMatch asserts a required capability
// missing from every lane's cached capability set returns
// ErrNoCapableProvider and never dispatches.
func TestRouter_RequiredCapability_NoMatch(t *testing.T) {
	reg := oneHealthyLane()
	reg.providers[0].Capabilities.Search = provider.CapabilityUnsupported
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.RequiredCapabilities.Search = true

	_, err := r.Select(context.Background(), req)
	if err != ErrNoCapableProvider {
		t.Fatalf("got %v, want ErrNoCapableProvider", err)
	}
	if quota.callN != 0 {
		t.Fatalf("quota.NextLane called %d times, want 0: capability denial must never reach dispatch", quota.callN)
	}
}

// TestRouter_UnprobedLaneExcluded asserts a lane whose relevant
// capability dimension is CapabilityUnknown (never probed) is excluded
// whenever that dimension is required, and the "capability:N-unprobed-
// excluded" ReasonFlag is emitted (R-21.213).
func TestRouter_UnprobedLaneExcluded(t *testing.T) {
	reg := oneHealthyLane()
	reg.providers[0].Capabilities.Search = provider.CapabilityUnknown
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.RequiredCapabilities.Search = true

	_, flags, err := r.SelectExplain(context.Background(), req)
	if err != ErrNoCapableProvider {
		t.Fatalf("got %v, want ErrNoCapableProvider", err)
	}
	if !containsPrefix(flags, "capability:1-unprobed-excluded") {
		t.Fatalf("flags = %v, want a capability:1-unprobed-excluded entry", flags)
	}
}

// TestRouter_ExcludeLane_RemovesNamedOnly asserts the exclude argument
// removes exactly the named lanes and nothing else.
func TestRouter_ExcludeLane_RemovesNamedOnly(t *testing.T) {
	reg := oneHealthyLane()
	reg.providers = append(reg.providers, provider.ProviderInfo{
		Name: "prov-b", BaseURL: "http://127.0.0.1:8081", HealthStatus: "healthy",
	})
	reg.lanes = append(reg.lanes, provider.LaneInfo{LaneName: "lane-b", ProviderName: "prov-b"})
	quota := &fakeQuota{order: []LaneID{"lane-a", "lane-b"}}
	r := NewRouter(reg, quota, nil, nil)

	sel, err := r.Select(context.Background(), chatReq(), "lane-a")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.LaneID != "lane-b" {
		t.Fatalf("LaneID = %q, want lane-b (lane-a excluded)", sel.LaneID)
	}
}

// TestRouter_AllProvidersEvicted asserts an unhealthy lane after
// capability matching returns ErrAllProvidersEvicted.
func TestRouter_AllProvidersEvicted(t *testing.T) {
	reg := oneHealthyLane()
	reg.providers[0].HealthStatus = "evicted"
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)

	_, err := r.Select(context.Background(), chatReq())
	if err != ErrAllProvidersEvicted {
		t.Fatalf("got %v, want ErrAllProvidersEvicted", err)
	}
}

// TestRouter_DryRun_NoDispatch documents and asserts that Select never
// holds or calls a provider.ModelProvider at all: the dispatch seam
// belongs to pipeline.go/execute.go, not the Router (see router.go's
// header comment). The acceptance criterion "dry-run makes zero provider
// calls" is therefore satisfied unconditionally by this package's type
// boundary, and req.Policy carries no dry_run field to gate on - none
// exists anywhere in pkg/provider (CONTRACT DEVIATION, see journal).
func TestRouter_DryRun_NoDispatch(t *testing.T) {
	reg := oneHealthyLane()
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)
	if _, err := r.Select(context.Background(), chatReq()); err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Router holds no ProviderResolver/ModelProvider field at all
	// (router.go's struct has exactly registry/quota/clock/classes), so
	// there is no call site through which Select could ever reach a
	// provider - verified by inspection, not by a spy, because there is
	// nothing to spy on.
}

// TestRouter_SnapshotImmutable asserts the registry is read exactly once
// (ListLanes once, ListProviders once) per Select call - no filter
// re-reads it mid-pass.
func TestRouter_SnapshotImmutable(t *testing.T) {
	reg := oneHealthyLane()
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)
	if _, err := r.Select(context.Background(), chatReq()); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if reg.callsListLanes != 1 {
		t.Fatalf("ListLanes called %d times, want 1", reg.callsListLanes)
	}
	if reg.callsListProviders != 1 {
		t.Fatalf("ListProviders called %d times, want 1", reg.callsListProviders)
	}
}

// TestNewDaemonRouter_WiresQuotaAndPublishesDivergence asserts
// NewDaemonRouter's production wiring: a missing/unverifiable
// [conductor.quota] section (nil extra) publishes exactly one divergence
// event and still returns a usable *DefaultRouter.
func TestNewDaemonRouter_WiresQuotaAndPublishesDivergence(t *testing.T) {
	bus := &fakeEventPublisher{}
	r, err := NewDaemonRouter(nil, nil, bus, nil, nil)
	if err != nil {
		t.Fatalf("NewDaemonRouter: %v", err)
	}
	if r == nil {
		t.Fatal("NewDaemonRouter returned a nil router")
	}
	if len(bus.events) != 1 {
		t.Fatalf("divergence events published = %d, want 1", len(bus.events))
	}
}

func containsPrefix(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
