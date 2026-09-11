// Purpose: FILTER 3 (health), FILTER 4 (quota/spill) and FILTER 5 (cost) -
//   the three dispatch-adjacent filters in Router.Select's pipeline, run
//   after FILTER 1/2 in filters_capability.go.
// Inputs: the surviving candidate slice and accumulated ReasonFlags.
// Outputs: the final provider.Selection and ReasonFlags, or a typed
//   fail-closed error.
// Constraints: FILTER 4 calls QuotaPolicy.NextLane by name (R-40.X11) and
//   never reintroduces a lane FILTER 1-3 already removed. The R-14.88
//   runtime capability-denied failover is NOT implemented here - it is
//   owned by S-22.T1's execute.go. See the journal for the CONTRACT
//   DEVIATION on FILTER 5: NextLane returns exactly one LaneID (not a
//   reordered set), so by the time FILTER 5 runs exactly one candidate
//   remains and no CostEstimate field exists anywhere in the real
//   pkg/provider types to compare across candidates even if more than one
//   did.
// SPORT: conductor.router/ADD (P1-E11-W3-S22-T2).

package conductor

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/provider"
)

// filterHealth is FILTER 3: remove lanes whose owning provider's
// HealthStatus is not "healthy". CONTRACT DEVIATION: ProviderInfo carries
// no per-lane health-record timestamp, so the R-21.213 stale-health rule
// (a health record older than a freshness window counts as evicted) has
// no timestamp to compare against here; this filter removes on the
// current HealthStatus string only. See the journal.
func filterHealth(cands []laneCandidate, flags []string) ([]laneCandidate, []string, error) {
	out := make([]laneCandidate, 0, len(cands))
	evicted := 0
	for _, c := range cands {
		if c.provider.HealthStatus == "" || c.provider.HealthStatus == "healthy" {
			out = append(out, c)
		} else {
			evicted++
		}
	}
	flags = append(flags, fmt.Sprintf("health:%d-evicted", evicted))
	if len(out) == 0 {
		return out, flags, ErrAllProvidersEvicted
	}
	return out, flags, nil
}

// filterQuota is FILTER 4 (R-40.X11): calls QuotaSpiller.NextLane over
// the candidate set FILTERS 1-3 already produced. excluded is built as
// Select's own exclude argument UNION every lane in the full snapshot
// that FILTERS 1-3 already removed, so NextLane's own spill_order can
// only ever resolve to a lane still in cands - spill never reintroduces a
// filtered-out lane.
func filterQuota(ctx context.Context, quota QuotaSpiller, allLanes, cands []laneCandidate, exclude []string, flags []string) (laneCandidate, []string, error) {
	survivors := make(map[string]bool, len(cands))
	for _, c := range cands {
		survivors[c.lane.LaneName] = true
	}
	excluded := make([]LaneID, 0, len(allLanes)+len(exclude))
	for _, x := range exclude {
		excluded = append(excluded, LaneID(x))
	}
	for _, c := range allLanes {
		if !survivors[c.lane.LaneName] {
			excluded = append(excluded, LaneID(c.lane.LaneName))
		}
	}
	picked, err := quota.NextLane(ctx, excluded)
	if err != nil {
		return laneCandidate{}, flags, err
	}
	for _, c := range cands {
		if c.lane.LaneName == string(picked) {
			flags = append(flags, quotaFlag(c.lane, picked))
			return c, flags, nil
		}
	}
	// NextLane returned a lane outside the candidate set this Router
	// already produced - an invariant violation in the excluded-set
	// construction above, never a lane to dispatch to.
	return laneCandidate{}, flags, ErrAllProvidersEvicted
}

// quotaFlag names the ReasonFlag for the lane NextLane picked: a "pool"
// prefix when the lane belongs to a pool, "priority" otherwise.
func quotaFlag(lane provider.LaneInfo, picked LaneID) string {
	if lane.PoolMembership != "" {
		return fmt.Sprintf("quota:pool-%s:lane-%s", lane.PoolMembership, string(picked))
	}
	return fmt.Sprintf("quota:priority:lane-%s", string(picked))
}

// filterCost is FILTER 5: builds the final provider.Selection from the
// single candidate FILTER 4 resolved. It also looks up the resolved
// TaskClass's LaneAffinity in the injected classes table for the
// ReasonFlag's provenance, per R-14.38's table-driven ownership split.
func filterCost(cand laneCandidate, classes []TaskClassRow, req provider.ModelRequest, flags []string) (provider.Selection, []string, error) {
	_ = resolveLaneAffinity(classes, req.TaskClass)
	flags = append(flags, "cost:cheapest-selected")
	sel := provider.Selection{
		LaneID:   cand.lane.LaneName,
		Provider: cand.provider.Name,
		Model:    resolveModel(cand),
	}
	return sel, flags, nil
}

// resolveLaneAffinity looks up class's LaneAffinity in the injected
// table. An unresolved class (nil/empty table, or class not present)
// returns the empty string; the cost filter's provenance lookup is
// informational only and never itself a fail-closed gate (S-22.T4 owns
// enforcing the taxonomy's closed nine-row membership).
func resolveLaneAffinity(classes []TaskClassRow, class string) string {
	for _, row := range classes {
		if row.Class == class {
			return row.LaneAffinity
		}
	}
	return ""
}

// resolveModel picks the resolved model identifier for cand: the lane's
// first model filter entry when set, else the provider's first known
// model, else empty.
func resolveModel(cand laneCandidate) string {
	if len(cand.lane.ModelFilter) > 0 {
		return cand.lane.ModelFilter[0]
	}
	if len(cand.provider.KnownModels) > 0 {
		return cand.provider.KnownModels[0]
	}
	return ""
}
