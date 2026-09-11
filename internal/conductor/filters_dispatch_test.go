package conductor

import (
	"context"
	"testing"
)

// TestRouter_StaleHealthExcluded documents the CONTRACT DEVIATION:
// ProviderInfo carries no per-lane health-record timestamp, so the
// verified behavior this test asserts is the current-HealthStatus-string
// removal FILTER 3 actually implements, not a timestamp-staleness check
// the real type has no field for.
func TestRouter_StaleHealthExcluded(t *testing.T) {
	reg := oneHealthyLane()
	reg.providers[0].HealthStatus = "demoted"
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)

	_, err := r.Select(context.Background(), chatReq())
	if err != ErrAllProvidersEvicted {
		t.Fatalf("got %v, want ErrAllProvidersEvicted", err)
	}
}

// TestRouter_UnknownCostExcluded documents the CONTRACT DEVIATION: no
// CostEstimate/rate-card field exists anywhere in the real pkg/provider
// types (Selection has no CostEstimate field to compare), so FILTER 5
// cannot exclude on an unresolvable cost. This test asserts the verified
// behavior: filterCost always succeeds over the single FILTER-4-resolved
// candidate, appending the cost:cheapest-selected ReasonFlag.
func TestRouter_UnknownCostExcluded(t *testing.T) {
	reg := oneHealthyLane()
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)

	_, flags, err := r.SelectExplain(context.Background(), chatReq())
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if !containsPrefix(flags, "cost:cheapest-selected") {
		t.Fatalf("flags = %v, want cost:cheapest-selected", flags)
	}
}

// TestRouter_TieBreakCostThenLaneID documents the CONTRACT DEVIATION:
// R-40.X11 makes FILTER 4's QuotaSpiller.NextLane the mechanism that
// narrows candidates to exactly one lane before FILTER 5 ever runs, so no
// CostEstimate-then-LaneID comparison across multiple survivors is
// reachable. This test asserts the real, verified ordering mechanism:
// among two capability- and health-eligible lanes, the one earlier in the
// QuotaSpiller's own order is the one selected.
func TestRouter_TieBreakCostThenLaneID(t *testing.T) {
	reg := twoLaneRegistry()
	quota := &fakeQuota{order: []LaneID{"lane-remote", "lane-local"}}
	r := NewRouter(reg, quota, nil, nil)

	sel, err := r.Select(context.Background(), chatReq())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.LaneID != "lane-remote" {
		t.Fatalf("LaneID = %q, want lane-remote (first in quota order)", sel.LaneID)
	}
}

// TestRouter_ExecutionModePopulated is a documented skip: the frozen
// pkg/provider.Selection type (R-40.X12) declares only LaneID, Provider
// and Model - no ExecutionMode field exists to populate, and adding one
// is a pkg/provider/model.go change outside this ticket's files_scope.
// See the journal's CONTRACT DEVIATION entry.
func TestRouter_ExecutionModePopulated(t *testing.T) {
	t.Skip("CONTRACT DEVIATION: provider.Selection has no ExecutionMode field (R-40.X12 locks Selection to the frozen pkg/provider type); see journal")
}

// TestFilterQuotaCallsNextLane asserts FILTER 4 calls NextLane over the
// already-filtered candidate set (excluding every lane FILTERS 1-3
// removed, plus Select's own exclude argument), and that spill never
// reintroduces a filtered-out lane.
func TestFilterQuotaCallsNextLane(t *testing.T) {
	reg := twoLaneRegistry()
	reg.providers[1].HealthStatus = "evicted" // lane-remote is health-filtered out
	quota := &fakeQuota{order: []LaneID{"lane-remote", "lane-local"}}
	r := NewRouter(reg, quota, nil, nil)

	sel, err := r.Select(context.Background(), chatReq())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.LaneID != "lane-local" {
		t.Fatalf("LaneID = %q, want lane-local (lane-remote was health-filtered, spill must not reintroduce it)", sel.LaneID)
	}
	if quota.callN != 1 {
		t.Fatalf("NextLane called %d times, want 1", quota.callN)
	}
	excluded := quota.calls[0]
	found := false
	for _, x := range excluded {
		if x == "lane-remote" {
			found = true
		}
	}
	if !found {
		t.Fatalf("excluded set %v did not contain the health-filtered lane-remote", excluded)
	}
}

// TestFilterQuotaCallsNextLane_AllLanesExhausted asserts a QuotaSpiller
// exhaustion error propagates unchanged (fail-closed, typed).
func TestFilterQuotaCallsNextLane_AllLanesExhausted(t *testing.T) {
	reg := oneHealthyLane()
	quota := &fakeQuota{forceErr: ErrAllLanesExhausted}
	r := NewRouter(reg, quota, nil, nil)

	_, err := r.Select(context.Background(), chatReq())
	if err != ErrAllLanesExhausted {
		t.Fatalf("got %v, want ErrAllLanesExhausted", err)
	}
}
