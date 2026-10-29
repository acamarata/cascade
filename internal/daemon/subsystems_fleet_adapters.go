package daemon

// Purpose: the capacity step's adapters: learn's decision writer as the Fleet's
// DecisionRecorder, registry lanes to quota domains, and node placement over the
// enrolled records. Constraints: fail closed; no second reader or store.
// SPORT: internal/daemon (ADD, P1-CAP-05).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LearnDecisionRecorder adapts learn's decision writer to capacity.DecisionRecorder (Bind's transaction).
type LearnDecisionRecorder struct{ Writer learn.SchedulerDecisionWriter }

// Record writes d through q as one jobs_scheduler_decisions row.
func (a LearnDecisionRecorder) Record(ctx context.Context, q capacity.DecisionExecer, d capacity.DecisionRecord) error {
	return a.Writer.Record(ctx, q, learn.SchedulerDecision{
		ID: d.ID, JobID: d.JobID, ExecutionID: d.ExecutionID, TaskClass: string(d.TaskClass),
		SelectedTier: string(d.SelectedTier), SelectedLaneID: string(d.LaneID), SelectedNodeID: d.NodeID,
		ScoreAtSelection: d.ScoreAtSelection, FallbackLevel: learn.FallbackLevel(d.FallbackLevel),
		JumpRuleFired: d.JumpRuleFired, JumpReasonCode: d.JumpReasonCode, ReserveTier0: d.ReserveTier0, DecidedAt: d.DecidedAt,
	})
}

// RegistryLaneDomains maps a registry lane to its quota domain: Reconcile names
// each credential after its lane, so it resolves via topology.Store.DomainForCredential.
type RegistryLaneDomains struct{ Topology *topology.Store }

// DomainOf returns lane's quota domain; an absent or quarantined credential and a
// domain with no live lane refuse capacity.ErrNoQuotaDomain, a failed read passes through.
func (m RegistryLaneDomains) DomainOf(ctx context.Context, lane conductor.LaneID) (string, error) {
	if strings.HasPrefix(string(lane), "agent:") { // DEBT P1-AGT-08: agent lanes have no quota domain yet
		return "", cascade.Wrapf(cascade.KindUnavailable, capacity.ErrNoQuotaDomain, "lane %q has no quota domain", lane)
	}
	id, err := m.Topology.DomainForCredential(ctx, topology.CredentialID(lane))
	if err == nil {
		return string(id), nil
	}
	if cascade.HasKind(err, cascade.KindNotFound) || chainHas(err, topology.ErrNoLiveQuotaDomain) {
		return "", cascade.Wrapf(cascade.KindUnavailable, capacity.ErrNoQuotaDomain, "lane %q: %v", lane, err)
	}
	return "", err
}

// NodePlacer places a requirement over the enrolled records with the placement engine, reading the
// tunnel lookup at call time. DEBT: until SetNodeTunnels is called every node reads unconnected.
type NodePlacer struct {
	Records *nodes.RecordStore
	Tunnels func() nodes.TunnelStateLookup
}

// Place returns the first eligible enrolled node for need.
func (p NodePlacer) Place(_ context.Context, need nodes.Requirement) (string, error) {
	recs, err := p.Records.List()
	if err != nil {
		return "", err
	}
	engine := nodes.Engine{}
	if p.Tunnels != nil {
		engine.Tunnels = p.Tunnels()
	}
	eligible, err := engine.Eligible(need, nodes.CandidatesFrom(recs))
	if err != nil {
		return "", err
	}
	if len(eligible) == 0 {
		return "", cascade.New(cascade.KindUnavailable, "fleet runtime: no eligible node")
	}
	return eligible[0].NodeID, nil
}

// recordList is a fixed node record list as a capacity.NodeSource.
type recordList []nodes.DeviceRecord

// List returns the records.
func (l recordList) List() ([]nodes.DeviceRecord, error) { return l, nil }

// nodesSignature is the part of the records a NodeSlot is built from (id, presence, tier), in order.
func nodesSignature(recs []nodes.DeviceRecord) string {
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "%s|%s|%s;", r.NodeID, r.Presence, r.Tier)
	}
	return b.String()
}

// chainHas reports whether target is in err's chain by identity: errors.Is on a
// cascade error compares Kind only, so any unavailable read would match.
func chainHas(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}

// withSelf returns recs plus a reachable record for the controller itself (self has no
// enrolled record; the sampler fills its load), unless the records already name it.
func withSelf(recs []nodes.DeviceRecord, self string) recordList {
	out := recordList(recs)
	if self == "" {
		return out
	}
	for _, r := range recs {
		if r.NodeID == self {
			return out
		}
	}
	return append(out[:len(out):len(out)], nodes.DeviceRecord{NodeID: self, Presence: nodes.PresenceReachable})
}
