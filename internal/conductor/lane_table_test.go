package conductor

// Purpose (this file): the one (tier x lane type) table and everything that
//   must agree with it - the router's sensitivity filter, the thread-privacy
//   filter, the restricted-permit parser and the lane classifier's hostile
//   inputs.
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

// TestSensitivityAllowsTable asserts every cell by naming each expected
// answer, never by re-deriving it from the switch under test. Columns, in
// allLaneTypes order: controller-local, external-api, unresolved, undeclared.
func TestSensitivityAllowsTable(t *testing.T) {
	type row struct{ unpermitted, permitted [4]bool }
	want := map[string]row{
		"local-only": {[4]bool{true, false, false, false}, [4]bool{true, false, false, false}},
		// The permit widens ONE cell: restricted x external-api.
		"restricted":   {[4]bool{true, false, false, false}, [4]bool{true, true, false, false}},
		"internal":     {[4]bool{true, true, false, false}, [4]bool{true, true, false, false}},
		"public":       {[4]bool{true, true, true, true}, [4]bool{true, true, true, true}},
		"out-of-range": {[4]bool{true, false, false, false}, [4]bool{true, true, false, false}},
	}
	cells := 0
	for _, m := range allModes {
		r, ok := want[m.name]
		if !ok {
			t.Fatalf("no expectation declared for tier %s", m.name)
		}
		for i, lane := range allLaneTypes {
			for _, permitted := range []bool{false, true} {
				cells++
				exp := r.unpermitted[i]
				if permitted {
					exp = r.permitted[i]
				}
				if got := SensitivityAllows(m.mode, lane, permitted); got != exp {
					t.Errorf("SensitivityAllows(%s, %s, permitted=%v) = %v, want %v", m.name, lane, permitted, got, exp)
				}
			}
		}
	}
	if cells != len(allModes)*len(allLaneTypes)*2 {
		t.Fatalf("checked %d cells, want %d: the table did not resolve", cells, len(allModes)*len(allLaneTypes)*2)
	}
	// The two named invalid tiers read as restricted: they are not tiers.
	for _, bad := range []provider.SensitivityTier{4, 255} {
		if bad.Valid() {
			t.Fatalf("fixture bug: tier %d validates", bad)
		}
		if SensitivityAllows(bad, LaneExternalAPI, false) {
			t.Errorf("tier %d x external-api without a permit allowed; an invalid tier must read as restricted", bad)
		}
		if !SensitivityAllows(bad, LaneExternalAPI, true) {
			t.Errorf("tier %d x external-api with a permit refused; it reads as restricted, which the permit widens", bad)
		}
		if SensitivityAllows(bad, LaneUnresolved, true) {
			t.Errorf("tier %d x unresolved allowed; a permit never admits an unresolved lane", bad)
		}
	}
}

// internalReq is a chat request at the internal tier: tests whose subject is
// quota, cost or ordering over a local AND a remote lane use it, because a
// default (restricted) request now removes the remote lane before they run.
func internalReq() provider.ModelRequest {
	req := chatReq()
	req.Sensitivity = provider.SensitivityInternal
	return req
}

// laneCand builds a candidate for a lane served at baseURL.
func laneCand(lane, baseURL string) laneCandidate {
	return laneCandidate{
		lane:     provider.LaneInfo{LaneName: lane, ProviderName: "p-" + lane},
		provider: provider.ProviderInfo{Name: "p-" + lane, BaseURL: baseURL, HealthStatus: "healthy"},
	}
}

func permitsOf(t *testing.T, lanes ...string) RestrictedPermits {
	t.Helper()
	items := make([]interface{}, len(lanes))
	for i, l := range lanes {
		items[i] = l
	}
	p, err := ParseRestrictedPermits(map[string]interface{}{"restricted_external_lanes": items})
	if err != nil {
		t.Fatalf("ParseRestrictedPermits(%v): %v", lanes, err)
	}
	return p
}

// TestAdmitLane covers the single-lane form: an admitted lane returns no
// flag and no error; a refused one returns both, and the error is the
// frozen sentinel by kind AND by message.
func TestAdmitLane(t *testing.T) {
	remote := laneCand("lane-remote", "https://api.example.com")
	flag, err := admitLane(remote, provider.SensitivityRestricted, RestrictedPermits{})
	if flag != "sensitivity:restricted:removed:lane-remote(external-api)" {
		t.Errorf("flag = %q", flag)
	}
	if err == nil || !errors.Is(err, ErrSensitivityViolation) || !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want a KindPolicyDenied ErrSensitivityViolation", err)
	}
	for _, want := range []string{"restricted", "lane-remote", "external-api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err.Error(), want)
		}
	}
	if flag, err := admitLane(remote, provider.SensitivityRestricted, permitsOf(t, "lane-remote")); flag != "" || err != nil {
		t.Errorf("permitted lane: flag %q err %v, want both empty", flag, err)
	}
	// A permit names a LANE: another lane's name admits nothing.
	if _, err := admitLane(remote, provider.SensitivityRestricted, permitsOf(t, "lane-other")); err == nil {
		t.Error("a permit for a different lane admitted lane-remote")
	}
	// A permit never admits an unresolved lane.
	blank := laneCand("lane-blank", "")
	if _, err := admitLane(blank, provider.SensitivityRestricted, permitsOf(t, "lane-blank")); err == nil {
		t.Error("a permit admitted a lane with no resolvable host")
	}
}

// TestPrivacyAndRouterAgree runs every tier x lane type through BOTH
// filters (FILTER 2 with the request tier, FILTER 0 with a thread of the
// same tier) and requires the same verdict, with and without a permit.
func TestPrivacyAndRouterAgree(t *testing.T) {
	lanes := map[string]string{
		"controller-local": "http://127.0.0.1:8080",
		"external-api":     "https://api.example.com",
		"unresolved":       "",
	}
	checked := 0
	for _, m := range allModes {
		for laneType, baseURL := range lanes {
			for _, permitted := range []bool{false, true} {
				cands := []laneCandidate{laneCand("lane-x", baseURL)}
				permits := RestrictedPermits{}
				if permitted {
					permits = permitsOf(t, "lane-x")
				}
				req := chatReq()
				req.Sensitivity = m.mode
				keptR, _, errR := filterSensitivity(cands, req, permits, nil)
				ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{ThreadID: "th", Mode: m.mode})
				keptP, _, errP := filterPrivacy(ctx, cands, permits, nil)
				allowed := len(keptR) == 1
				if allowed != (len(keptP) == 1) || (errR == nil) != (errP == nil) {
					t.Errorf("%s x %s permit=%v: router kept=%d err=%v, privacy kept=%d err=%v", m.name, laneType, permitted, len(keptR), errR, len(keptP), errP)
				}
				// And both equal the table itself.
				lt := ClassifyLane(cands[0].provider)
				if lt.String() != laneType {
					t.Fatalf("fixture bug: %q classified %s, want %s", baseURL, lt, laneType)
				}
				if allowed != SensitivityAllows(m.mode, lt, permitted) {
					t.Errorf("%s x %s permit=%v: filters allowed=%v, table says otherwise", m.name, laneType, permitted, allowed)
				}
				checked++
			}
		}
	}
	if checked != len(allModes)*3*2 {
		t.Fatalf("checked %d combinations, want %d", checked, len(allModes)*3*2)
	}
}

// TestPrivacyRefusalNamesRemovedLanesAndKey: the thread-privacy path gives
// the same explain trail as FILTER 2 - one removed flag per refused lane, and
// a refusal that names the permit key.
func TestPrivacyRefusalNamesRemovedLanesAndKey(t *testing.T) {
	reg := &fakeRegistry{
		providers: []provider.ProviderInfo{
			{Name: "pa", BaseURL: "https://a.example.com", HealthStatus: "healthy"},
			{Name: "pb", BaseURL: "https://b.example.com", HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: "lane-a", ProviderName: "pa", State: "active"},
			{LaneName: "lane-b", ProviderName: "pb", State: "active"},
		},
	}
	r := NewRouter(reg, &fakeQuota{order: []LaneID{"lane-a", "lane-b"}}, nil, nil)
	ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{ThreadID: "th-r", Mode: provider.SensitivityRestricted})
	_, flags, err := r.SelectExplain(ctx, chatReq())
	if err == nil || !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("err = %v, want ErrSensitivityViolation", err)
	}
	for _, want := range []string{"th-r", "conductor.restricted_external_lanes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err.Error(), want)
		}
	}
	for _, want := range []string{"privacy:restricted:2-filtered", "sensitivity:restricted:removed:lane-a(external-api)", "sensitivity:restricted:removed:lane-b(external-api)"} {
		if !hasFlagPrefix(flags, want) {
			t.Errorf("flags = %v, want %q", flags, want)
		}
	}
}
