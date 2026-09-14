// Purpose: the routing-matrix fail-closed edge rows this ticket's
//   acceptance criteria require (unknown/zero task class, unresolvable
//   sensitivity, no capable provider), plus the cost-tie unreachability
//   finding: T5's own task text asks for a "cost-tie" fixture that the
//   real Select pipeline structurally cannot produce (see this package's
//   testdata/routing_matrix/README.md).
// Inputs: none - constructs its own minimal fixtures per row.
// Outputs: none - test file.
// Constraints: drives the real production entry points execute.go's
//   authorize sequence actually calls (ResolveTaskClass, ResolveSensitivity,
//   Router.Select) - never a bare assertion re-implementing their logic.
// SPORT: conductor.router/ADD (P1-E11-W3-S23-T5).

package conductor

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestRoutingMatrixEdge_UnknownTaskClass asserts the fail-closed rule
// execute.go's authorize (execute.go:142) relies on: ResolveTaskClass -
// the real function that call site invokes - returns exactly
// ErrInvalidRequest for an empty task_class and for an unknown one,
// never a permissive default (R-21.208 terminal deny).
func TestRoutingMatrixEdge_UnknownTaskClass(t *testing.T) {
	cases := []string{"", "not-a-real-class", "CHAT"} // empty, unknown, wrong-case
	for _, tc := range cases {
		req := chatReq()
		req.TaskClass = tc
		if _, err := ResolveTaskClass(req); err != ErrInvalidRequest {
			t.Errorf("ResolveTaskClass(TaskClass=%q) = %v, want ErrInvalidRequest", tc, err)
		}
	}
}

// TestRoutingMatrixEdge_UnresolvableSensitivity asserts the fail-closed
// rule execute.go's authorize (execute.go:135) relies on first:
// ResolveSensitivity - the real function that call site invokes -
// resolves an unset request and an out-of-range tier value to
// SensitivityRestricted, never to a looser tier.
func TestRoutingMatrixEdge_UnresolvableSensitivity(t *testing.T) {
	unset := provider.ModelRequest{} // zero value: Sensitivity is unset
	if got := ResolveSensitivity(unset); got != provider.SensitivityRestricted {
		t.Errorf("ResolveSensitivity(unset) = %v, want SensitivityRestricted", got)
	}

	unresolvable := chatReq()
	unresolvable.Sensitivity = provider.SensitivityTier(200) // outside the four declared members
	if unresolvable.Sensitivity.Valid() {
		t.Fatal("test fixture bug: SensitivityTier(200) validates as a declared member")
	}
	if got := ResolveSensitivity(unresolvable); got != provider.SensitivityRestricted {
		t.Errorf("ResolveSensitivity(unresolvable=200) = %v, want SensitivityRestricted", got)
	}
}

// TestRoutingMatrixEdge_NoCapableProvider asserts the real Router.Select
// entry point - the same method execute.go:102 calls - returns exactly
// ErrNoCapableProvider, and dispatches zero times (fakeQuota.callN stays
// 0), when no lane's cached capability set satisfies a required
// dimension. This is the same outcome TestRouter_RequiredCapability_NoMatch
// in router_test.go already covers; restated here as this ticket's own
// acceptance row so the matrix's fail-closed edges are self-contained.
func TestRoutingMatrixEdge_NoCapableProvider(t *testing.T) {
	reg := oneHealthyLane()
	reg.providers[0].Capabilities.Search = provider.CapabilityUnsupported
	quota := &fakeQuota{order: []LaneID{"lane-a"}}
	r := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.RequiredCapabilities.Search = true

	_, err := r.Select(context.Background(), req)
	if err != ErrNoCapableProvider {
		t.Fatalf("Select: err = %v, want ErrNoCapableProvider", err)
	}
	if quota.callN != 0 {
		t.Fatalf("quota.NextLane called %d times, want 0: capability denial must never reach dispatch", quota.callN)
	}
}

// TestRoutingMatrix_CostTieUnreachable pins the finding documented in
// testdata/routing_matrix/README.md: T5's task list asks for a
// "cost-cheapest-selected and cost-tie" fixture pair, but filterCost's
// own signature (filters_dispatch.go) takes exactly one laneCandidate,
// never a slice - by the time FILTER 5 runs, FILTER 4 has already
// collapsed the candidate set to one lane (QuotaSpiller.NextLane returns
// exactly one LaneID, router.go:49-51). No legal call to filterCost can
// ever present two candidates for a cost comparison; this is a structural
// fact about the function's type signature, not a runtime observation.
// This test proves the reachable half of that finding: filterCost's own
// output never varies by which single lane survives - it always emits
// the identical literal flag, regardless.
func TestRoutingMatrix_CostTieUnreachable(t *testing.T) {
	req := chatReq()
	for _, laneName := range []string{"lane-a", "lane-b", "lane-with-a-very-different-name"} {
		cand := laneCandidate{
			lane:     provider.LaneInfo{LaneName: laneName},
			provider: provider.ProviderInfo{Name: "prov-x"},
		}
		sel, flags, err := filterCost(cand, nil, req, nil)
		if err != nil {
			t.Fatalf("filterCost(%s): %v", laneName, err)
		}
		if sel.LaneID != laneName {
			t.Fatalf("filterCost(%s): Selection.LaneID = %q, want %q", laneName, sel.LaneID, laneName)
		}
		if len(flags) != 1 || flags[0] != "cost:cheapest-selected" {
			t.Fatalf("filterCost(%s): flags = %v, want exactly [cost:cheapest-selected] - no tie branch exists", laneName, flags)
		}
	}
}
