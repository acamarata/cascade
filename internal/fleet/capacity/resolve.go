// Purpose: Fleet.Lease's read-only half. It turns a ResourceRequest and a
// DispatchRef into a leasePlan (lanes, tier, node, quota domain, ids) without
// writing a single row: allowed lanes from the snapshot, the sensitivity
// floor, the pluggable floors, the Selector, placement and the quota-domain
// lookup, in that order.
//
// Inputs: the request, the dispatch reference and the attempt number.
// Outputs: a leasePlan, or the first typed refusal.
// Constraints: nothing is written here; every refusal leaves zero rows. The
// sensitivity filter is conductor.SensitivityAllows (the one table)
// and always runs first; the Floors only narrow what it left. The joined
// request tier is JoinSensitivity(req.Sensitivity, TierOrLocalOnly(DataClass)),
// and the placement requirement carries that same tier.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// agentLanePrefix names the lanes AgentLaneSlots registers ("agent:<driver>").
const agentLanePrefix = "agent:"

// globalScorerKey is the scorer scope every request reads (the global level).
const globalScorerKey = "global"

// fallbackLevelGlobal is the posterior level a decision row records.
const fallbackLevelGlobal = "global"

// leasePlan is everything Lease decided before the first write.
type leasePlan struct {
	req      ResourceRequest
	d        DispatchRef
	attempt  int
	tier     provider.SensitivityTier
	sel      Selection
	node     string
	domain   string
	estimate economics.Estimate
	ids      leaseIDs
}

// leaseIDs are the identifiers minted for one lease.
type leaseIDs struct{ lease, execution, decision string }

// plan runs the read-only half of Lease.
func (f *Fleet) plan(ctx context.Context, req ResourceRequest, d DispatchRef, attempt int) (leasePlan, error) {
	if err := validateDispatch(d, attempt); err != nil {
		return leasePlan{}, err
	}
	floor, err := f.derivedFloor(ctx, d.JobID)
	if err != nil {
		return leasePlan{}, err
	}
	req, err = ValidateResourceRequest(req, floor)
	if err != nil {
		return leasePlan{}, err
	}
	est, err := economics.EstimateFor(d.HydratedTokens, req.TaskClass, d.Requests)
	if err != nil {
		return leasePlan{}, err
	}
	p := leasePlan{req: req, d: d, attempt: attempt, estimate: est, tier: joinedTier(req)}
	if p.sel, err = f.choose(ctx, p); err != nil {
		return leasePlan{}, err
	}
	if p.node, err = f.place(ctx, d, p.tier); err != nil {
		return leasePlan{}, err
	}
	if p.domain, err = f.domainFor(ctx, p.sel.Lane); err != nil {
		return leasePlan{}, err
	}
	if p.ids, err = mintLeaseIDs(); err != nil {
		return leasePlan{}, err
	}
	return p, nil
}

// joinedTier is the request's resolved sensitivity: the most restrictive of
// the declared sensitivity and the data class (nil reads local-only).
func joinedTier(req ResourceRequest) provider.SensitivityTier {
	return provider.JoinSensitivity(req.Sensitivity, provider.TierOrLocalOnly(req.DataClass))
}

// derivedFloor is the R-21.143 data-class floor: the leased job's own data
// class. A request with no job has nothing to derive from and reads
// local-only (nil), the narrowest tier.
func (f *Fleet) derivedFloor(ctx context.Context, jobID string) (*provider.SensitivityTier, error) {
	if jobID == "" {
		return nil, nil
	}
	job, ok, err := f.deps.Jobs.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "capacity: job %q does not exist", jobID)
	}
	return job.DataClass, nil
}

// choose reads the snapshot, narrows the lanes and asks the Selector.
func (f *Fleet) choose(ctx context.Context, p leasePlan) (Selection, error) {
	read, err := f.deps.Snapshot(ctx)
	if err != nil {
		return Selection{}, refuse(ErrNoCapableTier, err, "read the capacity snapshot")
	}
	snap := isolate(read) // Floors and the Selector never touch the snapshot owner's maps
	eligible, err := f.eligibleLanes(snap, p)
	if err != nil {
		return Selection{}, err
	}
	facts := captureFacts(snap, eligible)
	in := SelectInput{Snapshot: snap, Eligible: slices.Clone(eligible), Request: p.req, Scorer: f.deps.Scorer(globalScorerKey), Attempt: p.attempt}
	sel, err := f.deps.Selector.Select(ctx, in)
	if err != nil {
		return Selection{}, selectRefusal(err)
	}
	return verifySelection(facts, sel)
}

// eligibleLanes is allowed lanes, then the sensitivity filter, then Floors.
func (f *Fleet) eligibleLanes(snap FleetSnapshot, p leasePlan) ([]conductor.LaneID, error) {
	allowed := allowedLanes(snap, p.d.AgentOnly)
	if len(allowed) == 0 {
		return nil, ErrNoAllowedLanes
	}
	kept, removed := f.filterSensitivity(snap, allowed, p.tier)
	if len(kept) == 0 {
		return nil, sensitivityRefusal(p.tier, removed)
	}
	kept, removed = f.applyFloors(snap, kept, p.req)
	if len(kept) == 0 {
		return nil, refuse(ErrNoCapableTier, nil, "lane floors removed every lane: %s", describeRemoved(removed))
	}
	return kept, nil
}

// allowedLanes are the snapshot lanes whose credentials carry a programmatic
// entitlement, restricted to agent lanes when agentOnly and to the others
// otherwise. Sorted, never nil.
func allowedLanes(snap FleetSnapshot, agentOnly bool) []conductor.LaneID {
	out := make([]conductor.LaneID, 0, len(snap.Lanes))
	for id, slot := range snap.Lanes {
		if slot.Posture.ProgrammaticEntitlement && strings.HasPrefix(string(id), agentLanePrefix) == agentOnly {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// filterSensitivity keeps the lanes conductor.SensitivityAllows admits for
// tier and reports the rest with their lane type.
func (f *Fleet) filterSensitivity(snap FleetSnapshot, ids []conductor.LaneID, tier provider.SensitivityTier) ([]conductor.LaneID, []removedLane) {
	kept := make([]conductor.LaneID, 0, len(ids))
	var removed []removedLane
	for _, id := range ids {
		lt := snap.Lanes[id].LaneType
		if conductor.SensitivityAllows(tier, lt, f.deps.RestrictedPermits.Permits(string(id))) {
			kept = append(kept, id)
			continue
		}
		removed = append(removed, removedLane{lane: id, kind: lt.String()})
	}
	return kept, removed
}

// applyFloors runs every pluggable floor over the lanes the sensitivity
// filter kept. A lane any floor refuses is removed.
func (f *Fleet) applyFloors(snap FleetSnapshot, ids []conductor.LaneID, req ResourceRequest) ([]conductor.LaneID, []removedLane) {
	kept := make([]conductor.LaneID, 0, len(ids))
	var removed []removedLane
	for _, id := range ids {
		slot := snap.Lanes[id]
		if name, reason, ok := admitAll(f.deps.Floors, req, slot); !ok {
			removed = append(removed, removedLane{lane: id, kind: slot.LaneType.String(), reason: name + ": " + reason})
			continue
		}
		kept = append(kept, id)
	}
	return kept, removed
}

// admitAll reports the first floor that refuses slot.
func admitAll(floors []LaneFilter, req ResourceRequest, slot ProviderSlot) (name, reason string, ok bool) {
	for _, fl := range floors {
		if admitted, why := fl.Admit(req, slot); !admitted {
			return fl.Name(), why, false
		}
	}
	return "", "", true
}

// sensitivityRefusal explains an emptied lane set: local-only work is a
// placement constraint, every other tier is a data-class denial.
func sensitivityRefusal(tier provider.SensitivityTier, removed []removedLane) error {
	if tier == provider.SensitivityLocalOnly {
		return refuse(ErrSensitivityConstraint, nil, "%s work stays on the controller machine; removed lanes: %s", tier, describeRemoved(removed))
	}
	return refuse(ErrDataClassDenied, nil, "%s content may not use any remaining lane; removed lanes: %s", tier, describeRemoved(removed))
}

// selectRefusal maps a Selector error to the typed refusal Lease returns. The
// typed refusals a Selector already chose (ErrNoAllowedLanes, ErrNoCapableTier,
// ErrTier0Reserved) pass through unchanged; an exhausted walk is no capable
// tier.
func selectRefusal(err error) error {
	switch {
	case hasSentinel(err, ErrNoAllowedLanes), hasSentinel(err, ErrNoCapableTier), hasSentinel(err, ErrTier0Reserved):
		return err
	case hasSentinel(err, ErrFleetExhausted):
		return refuse(ErrNoCapableTier, err, "")
	}
	return refuse(ErrNoCapableTier, err, "selector failed")
}

// place resolves the node. A nil Placement runs on the controller; otherwise
// nodes placement decides, for a requirement whose tier is the request's
// joined tier (never supplied by the caller).
func (f *Fleet) place(ctx context.Context, d DispatchRef, tier provider.SensitivityTier) (string, error) {
	if d.Placement == nil {
		return f.deps.ControllerNodeID, nil
	}
	need := nodes.Requirement{Capabilities: slices.Clone(d.Placement.Capabilities), Sensitivity: tier}
	node, err := f.deps.Place(ctx, need)
	if err != nil {
		return "", placementRefusal(tier, err)
	}
	if node == "" {
		return "", placementRefusal(tier, cascade.New(cascade.KindUnavailable, "placement returned no node"))
	}
	if tier == provider.SensitivityLocalOnly && node != f.deps.ControllerNodeID {
		return "", refuse(ErrSensitivityConstraint, nil, "local-only work may not run on node %q", node)
	}
	return node, nil
}

// placementRefusal types a placement failure: restricted and local-only work
// failing placement is a sensitivity constraint, anything else is no capacity.
func placementRefusal(tier provider.SensitivityTier, cause error) error {
	if tier.MoreRestrictiveThan(provider.SensitivityInternal) {
		return refuse(ErrSensitivityConstraint, cause, "no node may run %s work", tier)
	}
	return refuse(ErrNoCapableTier, cause, "no node can run the work")
}

// domainFor resolves the selected lane's quota domain. A lane with no domain
// refuses: unknown capacity is never unlimited.
func (f *Fleet) domainFor(ctx context.Context, lane conductor.LaneID) (string, error) {
	domain, err := f.deps.DomainOf(ctx, lane)
	switch {
	case err == nil && domain != "":
		return domain, nil
	case err == nil, hasSentinel(err, ErrNoQuotaDomain), cascade.HasKind(err, cascade.KindNotFound):
		return "", refuse(ErrNoQuotaDomain, err, "lane %q", lane)
	}
	return "", refuse(ErrReservationFailed, err, "resolve the quota domain of lane %q", lane)
}

// mintLeaseIDs mints the lease, execution and decision ids.
func mintLeaseIDs() (leaseIDs, error) {
	var out [3]string
	for i := range out {
		id, err := cascade.NewID()
		if err != nil {
			return leaseIDs{}, err
		}
		out[i] = id.String()
	}
	return leaseIDs{lease: out[0], execution: out[1], decision: out[2]}, nil
}
