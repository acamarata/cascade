package conductor

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// twoLaneRegistry builds a registry with one controller-local lane
// ("lane-local", loopback BaseURL) and one remote lane ("lane-remote").
func twoLaneRegistry() *fakeRegistry {
	return &fakeRegistry{
		providers: []provider.ProviderInfo{
			{Name: "prov-local", BaseURL: "http://127.0.0.1:9000", HealthStatus: "healthy"},
			{Name: "prov-remote", BaseURL: "https://api.example.invalid", HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: "lane-local", ProviderName: "prov-local"},
			{LaneName: "lane-remote", ProviderName: "prov-remote"},
		},
	}
}

// TestRouter_SensitivityGate_LocalOnly_AdmitsLocalLane asserts a
// local-only request retains the local-locality lane and removes the
// remote one, emitting sensitivity:local-only:1-filtered (R-21.228).
func TestRouter_SensitivityGate_LocalOnly_AdmitsLocalLane(t *testing.T) {
	reg := twoLaneRegistry()
	quota := &fakeQuota{order: []LaneID{"lane-local", "lane-remote"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.Sensitivity = provider.SensitivityLocalOnly

	sel, flags, err := r.SelectExplain(context.Background(), req)
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if sel.LaneID != "lane-local" {
		t.Fatalf("LaneID = %q, want lane-local", sel.LaneID)
	}
	if !containsPrefix(flags, "sensitivity:local-only:1-filtered") {
		t.Fatalf("flags = %v, want sensitivity:local-only:1-filtered", flags)
	}
}

// TestRouter_SensitivityGate_LocalOnly_NoLocalLane_ErrNoLane asserts a
// local-only request with no local-locality lane returns ErrNoLane
// (R-21.217/R-21.228), never ErrSensitivityViolation.
func TestRouter_SensitivityGate_LocalOnly_NoLocalLane_ErrNoLane(t *testing.T) {
	reg := &fakeRegistry{
		providers: []provider.ProviderInfo{{Name: "prov-remote", BaseURL: "https://api.example.invalid", HealthStatus: "healthy"}},
		lanes:     []provider.LaneInfo{{LaneName: "lane-remote", ProviderName: "prov-remote"}},
	}
	quota := &fakeQuota{order: []LaneID{"lane-remote"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.Sensitivity = provider.SensitivityLocalOnly

	_, err := r.Select(context.Background(), req)
	if err != ErrNoLane {
		t.Fatalf("got %v, want ErrNoLane", err)
	}
}

// TestRouter_SensitivityGate_Restricted documents the CONTRACT DEVIATION:
// ProviderInfo carries no node-trust-tier vocabulary, so the restricted
// leg of FILTER 2 removes nothing; this test asserts that documented,
// verified behavior rather than a fabricated removal.
func TestRouter_SensitivityGate_Restricted(t *testing.T) {
	reg := twoLaneRegistry()
	quota := &fakeQuota{order: []LaneID{"lane-local"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.Sensitivity = provider.SensitivityRestricted

	_, flags, err := r.SelectExplain(context.Background(), req)
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if !containsPrefix(flags, "sensitivity:restricted:0-filtered") {
		t.Fatalf("flags = %v, want sensitivity:restricted:0-filtered", flags)
	}
}
