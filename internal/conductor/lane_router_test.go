package conductor

// Purpose (this file): the lane table through the real router: restricted
//   removes an unpermitted remote lane, a permit keeps it, an unknown tier
//   fails closed.
//
// SPORT: conductor.lane-table (TEST) - P1-SEC-02.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// restrictedRegistry offers one local and one remote lane.
func restrictedRegistry(remoteURL string) *fakeRegistry {
	return &fakeRegistry{
		providers: []provider.ProviderInfo{
			{Name: "prov-local", BaseURL: "http://127.0.0.1:9000", HealthStatus: "healthy"},
			{Name: "prov-remote", BaseURL: remoteURL, HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: "lane-local", ProviderName: "prov-local", State: "active"},
			{LaneName: "lane-remote", ProviderName: "prov-remote", State: "active"},
		},
	}
}

func TestRouterRestrictedRemovesUnpermittedRemote(t *testing.T) {
	t.Run("keeps only the local lane and names the removed one", restrictedKeepsLocalOnly)
	t.Run("a remote-only registry refuses and names the key", restrictedRemoteOnlyRefuses)
	t.Run("a permitted remote lane is kept", restrictedPermittedRemoteKept)
}

func restrictedReq() provider.ModelRequest {
	req := chatReq()
	req.Sensitivity = provider.SensitivityRestricted
	return req
}

func restrictedKeepsLocalOnly(t *testing.T) {
	// The remote lane is FIRST in the spill order: a gate that removed
	// nothing (the HEAD "0-filtered" branch) would select it.
	quota := &fakeQuota{order: []LaneID{"lane-remote", "lane-local"}}
	r := NewRouter(restrictedRegistry("https://api.example.com"), quota, nil, nil)
	sel, flags, err := r.SelectExplain(context.Background(), restrictedReq())
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if sel.LaneID != "lane-local" {
		t.Fatalf("LaneID = %q, want lane-local", sel.LaneID)
	}
	for _, want := range []string{"sensitivity:restricted:1-filtered", "sensitivity:restricted:removed:lane-remote(external-api)"} {
		if !hasFlagPrefix(flags, want) {
			t.Errorf("flags = %v, want %q", flags, want)
		}
	}
}

func restrictedRemoteOnlyRefuses(t *testing.T) {
	reg := restrictedRegistry("https://api.example.com")
	reg.providers, reg.lanes = reg.providers[1:], reg.lanes[1:]
	quota := &fakeQuota{order: []LaneID{"lane-remote"}}
	r := NewRouter(reg, quota, nil, nil)
	_, flags, err := r.SelectExplain(context.Background(), restrictedReq())
	if err == nil || !errors.Is(err, ErrSensitivityViolation) || !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied ErrSensitivityViolation", err)
	}
	for _, want := range []string{"restricted", `"lane-remote"`, "external-api", "1 candidate", "conductor.restricted_external_lanes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err.Error(), want)
		}
	}
	if !hasFlagPrefix(flags, "sensitivity:restricted:removed:lane-remote(external-api)") {
		t.Errorf("flags = %v, want the removed flag on the refusal path too", flags)
	}
	if quota.callN != 0 {
		t.Errorf("quota consulted %d times; a refused request reached dispatch", quota.callN)
	}
}

func restrictedPermittedRemoteKept(t *testing.T) {
	quota := &fakeQuota{order: []LaneID{"lane-remote", "lane-local"}}
	r := NewRouter(restrictedRegistry("https://api.example.com"), quota, nil, nil)
	// Apply the option the way the daemon's RegisterConductorRouter does.
	WithRestrictedPermits(permitsOf(t, "lane-remote"))(r)
	sel, flags, err := r.SelectExplain(context.Background(), restrictedReq())
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if sel.LaneID != "lane-remote" {
		t.Fatalf("LaneID = %q, want lane-remote (permitted)", sel.LaneID)
	}
	if !hasFlagPrefix(flags, "sensitivity:restricted:0-filtered") {
		t.Errorf("flags = %v, want sensitivity:restricted:0-filtered", flags)
	}
}

func TestRouterUnknownTierFailsClosed(t *testing.T) {
	reg := restrictedRegistry("https://api.example.com")
	reg.providers, reg.lanes = reg.providers[1:], reg.lanes[1:]
	for _, bad := range []provider.SensitivityTier{9, 255} {
		quota := &fakeQuota{order: []LaneID{"lane-remote"}}
		r := NewRouter(reg, quota, nil, nil)
		req := chatReq()
		req.Sensitivity = bad
		_, flags, err := r.SelectExplain(context.Background(), req)
		if err == nil || !errors.Is(err, ErrSensitivityViolation) {
			t.Fatalf("tier %d: err = %v, want ErrSensitivityViolation", bad, err)
		}
		if !strings.Contains(err.Error(), "restricted") {
			t.Errorf("tier %d: refusal %q does not say the tier read as restricted", bad, err.Error())
		}
		if !hasFlagPrefix(flags, "sensitivity:restricted:removed:lane-remote") {
			t.Errorf("tier %d: flags = %v", bad, flags)
		}
	}
}
