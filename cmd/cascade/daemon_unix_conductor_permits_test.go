//go:build !windows

// Purpose: proves the daemon reads [conductor] restricted_external_lanes at
// conductor construction: the config reaches the router through
// restrictedPermitsOption, a malformed list stops the daemon's conductor
// wiring, and an absent list leaves restricted content off every external
// lane.
// Inputs: hand-built runtime.Config values (no file, no vault, no network).
// Outputs: none.
// SPORT: cmd/cascade daemon (CHANGE, P1-SEC-02).
package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// permitsReader is a one-remote-lane registry.
type permitsReader struct{}

func (permitsReader) GetProvider(context.Context, string) (provider.ProviderInfo, error) {
	return provider.ProviderInfo{}, cascade.New(cascade.KindNotFound, "not used")
}

func (permitsReader) ListProviders(context.Context) ([]provider.ProviderInfo, error) {
	return []provider.ProviderInfo{{Name: "prov-cloud", BaseURL: "https://api.example.com", HealthStatus: "healthy"}}, nil
}

func (permitsReader) ListLanes(context.Context) ([]provider.LaneInfo, error) {
	return []provider.LaneInfo{{LaneName: "lane-cloud", ProviderName: "prov-cloud", State: "active"}}, nil
}

func (permitsReader) ListPool(context.Context, string) ([]provider.LaneInfo, error) { return nil, nil }

func (permitsReader) GetByModel(context.Context, string) ([]provider.ProviderInfo, error) {
	return nil, nil
}

// firstLaneSpiller offers the one lane.
type firstLaneSpiller struct{}

func (firstLaneSpiller) NextLane(context.Context, []conductor.LaneID) (conductor.LaneID, error) {
	return "lane-cloud", nil
}

func permitsConfig(extra map[string]interface{}) *runtime.Config {
	return &runtime.Config{Extra: extra}
}

// TestRestrictedPermitsOptionReachesTheRouter drives a real router built the
// way the daemon builds it: no list refuses a cloud lane for restricted
// content; the lane named in the config routes.
func TestRestrictedPermitsOptionReachesTheRouter(t *testing.T) {
	req := provider.ModelRequest{
		TaskID: "t1", TaskClass: "chat", Sensitivity: provider.SensitivityRestricted,
		Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	}
	for _, tc := range []struct {
		name    string
		cfg     *runtime.Config
		refused bool
	}{
		{"nil config", nil, true},
		{"no conductor section", permitsConfig(map[string]interface{}{}), true},
		{"conductor section without the key", permitsConfig(map[string]interface{}{"conductor": map[string]interface{}{"spill_enabled": true}}), true},
		{"another lane listed", permitsConfig(map[string]interface{}{"conductor": map[string]interface{}{
			"restricted_external_lanes": []interface{}{"lane-other"}}}), true},
		{"the cloud lane listed", permitsConfig(map[string]interface{}{"conductor": map[string]interface{}{
			"restricted_external_lanes": []interface{}{"lane-cloud"}}}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt, err := restrictedPermitsOption(tc.cfg)
			if err != nil {
				t.Fatalf("restrictedPermitsOption: %v", err)
			}
			r := conductor.NewRouter(permitsReader{}, firstLaneSpiller{}, nil, nil)
			opt(r)
			sel, err := r.Select(context.Background(), req)
			if tc.refused {
				if err == nil || !errors.Is(err, conductor.ErrSensitivityViolation) {
					t.Fatalf("Select = (%q, %v), want ErrSensitivityViolation", sel.LaneID, err)
				}
				if !strings.Contains(err.Error(), "conductor.restricted_external_lanes") {
					t.Errorf("refusal %q does not name the key", err.Error())
				}
				return
			}
			if err != nil || sel.LaneID != "lane-cloud" {
				t.Fatalf("Select = (%q, %v), want lane-cloud routed", sel.LaneID, err)
			}
		})
	}
}

// TestConductorWiringRefusesAMalformedPermitList runs the daemon's real
// "conductor" registration: a wildcard in the list stops the wiring with
// KindInvalidInput instead of starting a conductor with a list it cannot
// read exactly.
func TestConductorWiringRefusesAMalformedPermitList(t *testing.T) {
	var wire func(*daemonWiring) error
	for _, reg := range daemonRegistrations {
		if reg.Name == "conductor" {
			wire = reg.Wire
		}
	}
	if wire == nil {
		t.Fatal("no \"conductor\" daemon registration")
	}
	bad := permitsConfig(map[string]interface{}{"conductor": map[string]interface{}{
		"restricted_external_lanes": []interface{}{"*"}}})
	err := wire(&daemonWiring{Ctx: context.Background(), Deps: daemonRuntime{Config: bad}})
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Wire(wildcard list) = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "conductor.restricted_external_lanes") {
		t.Errorf("err %q does not name the key", err.Error())
	}
}
