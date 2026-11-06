package daemon

// Purpose: the fleet runtime's topology step. BindTopologyPass binds the
//
//	pass inputs once; ReconcileNow runs one serialized pass in the fixed
//	order below over the runtime's one topology.Store and QuotaStore and
//	returns step 1's topology.Report with RowsWritten raised by the rows
//	steps 2-7 really changed. A changing pass publishes one
//	fleet.topology.reconciled event. Health projects HealthKeys over the
//	live rows.
//
// Inputs: FleetRuntimeDeps (Config, Clock) and TopologyPassDeps.
// Outputs: Topology, BindTopologyPass, ReconcileNow, Health, SetPosteriorSource.
// Constraints: no second Store or QuotaStore; no goroutine; no row is
// deleted; a step error stops the pass and names the step; earlier steps'
// rows stay.
//
// SPORT: internal/daemon (ADD, P1-TOP-06e).

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The fleet.topology bus names. DEBT: these are the wire values of
// internal/rpc/fleet_topology_sse.go (FleetTopologyTopic,
// EventFleetTopologyReconciled, FleetTopologyReconciledPayload), which is
// not in this tree yet; switch to the rpc names once it lands.
const (
	fleetTopologyNamespace      = "fleet.topology"
	fleetTopologyAuditNamespace = "fleet.topology.audit"
	fleetTopologyReconciledKind = events.EventKind("fleet.topology.reconciled")
	fleetTopologyPromotionKind  = events.EventKind("fleet.topology.promotion")
	fleetTopologySource         = "fleet-topology"
)

// The pass's step names, in order.
var topologyStepNames = []string{"reconcile", "agent-lanes", "seed-buckets", "observation-buckets",
	"window-buckets", "lane-pins", "promotions"}

// TopologyPassDeps is what one topology pass reads. Every field is required.
type TopologyPassDeps struct {
	Registry     *registry.Registry
	AgentLanes   func() []topology.AgentLaneRecord
	Usage        provider.UsageReader
	QuotaWindows func(ctx context.Context, provider string) ([]usage.QuotaObservation, error)
	Bus          *events.Bus
	Clock        runtime.Clock
}

type fleetTopology struct {
	cfg        *runtime.Config
	clock      runtime.Clock
	mu         sync.Mutex // serializes passes, binding and the source
	bound      bool
	deps       TopologyPassDeps
	pins       []runtime.LaneReversalPin[topology.LaneClass]
	source     topology.PosteriorSource
	generation uint64
	beforeStep func(name string) error // test seam; nil in production
}

// reconciledWire is the fleet.topology.reconciled payload (see DEBT above).
type reconciledWire struct {
	SchemaVersion int    `json:"schema_version"`
	Generation    uint64 `json:"generation"`
	Lanes         int    `json:"lanes"`
	Changed       int    `json:"changed"`
}

// buildTopology is the topology step: it only records the config and clock.
func (r *FleetRuntime) buildTopology(_ context.Context, d FleetRuntimeDeps) error {
	r.topo.cfg, r.topo.clock = d.Config, d.Clock
	return nil
}

// Topology returns the runtime's one topology store.
func (r *FleetRuntime) Topology() *topology.Store { return r.res.topo }

// BindTopologyPass binds the pass inputs once (else KindConflict). A nil
// field or a malformed lane pin is KindInvalidInput and writes nothing.
func (r *FleetRuntime) BindTopologyPass(d TopologyPassDeps) error {
	t := &r.topo
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bound {
		return cascade.New(cascade.KindConflict, "fleet topology: the pass is already bound")
	}
	for _, dep := range []struct {
		name    string
		missing bool
	}{
		{"Registry", d.Registry == nil}, {"AgentLanes", d.AgentLanes == nil}, {"Usage", d.Usage == nil},
		{"QuotaWindows", d.QuotaWindows == nil}, {"Bus", d.Bus == nil}, {"Clock", d.Clock == nil},
	} {
		if dep.missing {
			return cascade.Newf(cascade.KindInvalidInput, "fleet topology: required dependency %s is nil", dep.name)
		}
	}
	pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](t.cfg, nil)
	if err != nil {
		return err
	}
	t.deps, t.pins, t.bound = d, pins, true
	return nil
}

// SetPosteriorSource binds the learned-evidence source once (else
// KindConflict); nil is KindInvalidInput.
func (r *FleetRuntime) SetPosteriorSource(src topology.PosteriorSource) error {
	t := &r.topo
	t.mu.Lock()
	defer t.mu.Unlock()
	if src == nil {
		return cascade.New(cascade.KindInvalidInput, "fleet topology: nil posterior source")
	}
	if t.source != nil {
		return cascade.New(cascade.KindConflict, "fleet topology: the posterior source is already set")
	}
	t.source = src
	return nil
}

// ReconcileNow runs one serialized pass (KindUnavailable before
// BindTopologyPass). On a step error the report so far and the error,
// wrapped with the step name, are returned.
func (r *FleetRuntime) ReconcileNow(ctx context.Context) (topology.Report, error) {
	t := &r.topo
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.bound {
		return topology.Report{}, cascade.New(cascade.KindUnavailable, "fleet topology: ReconcileNow before BindTopologyPass")
	}
	var rep topology.Report
	steps := r.topologySteps(&rep)
	total := 0
	for i, step := range steps {
		name := topologyStepNames[i]
		n, err := 0, error(nil)
		if t.beforeStep != nil {
			err = t.beforeStep(name)
		}
		if err == nil {
			n, err = step(ctx)
		}
		total += n
		if err != nil {
			rep.RowsWritten = total
			return rep, wrapKind(err, "fleet topology: step %q", name)
		}
	}
	rep.RowsWritten = total
	if total == 0 {
		return rep, nil
	}
	return rep, r.publishReconciled(ctx, total)
}

// topologySteps returns the seven steps in pass order; step 1 fills rep.
func (r *FleetRuntime) topologySteps(rep *topology.Report) []func(context.Context) (int, error) {
	d, store := r.topo.deps, r.res.topo
	return []func(context.Context) (int, error){
		func(ctx context.Context) (int, error) {
			got, err := topology.Reconcile(ctx, store, d.Registry, d.Clock)
			*rep = got
			return got.RowsWritten, err
		},
		func(ctx context.Context) (int, error) { return r.agentLanes(ctx, d) },
		func(ctx context.Context) (int, error) {
			return topology.SeedUndiscoveredBuckets(ctx, r.res.quota, store)
		},
		func(ctx context.Context) (int, error) { return r.observationBuckets(ctx, d) },
		func(ctx context.Context) (int, error) { return r.windowAccountantBuckets(ctx, d) },
		func(ctx context.Context) (int, error) { return r.lanePins(ctx, d) },
		func(ctx context.Context) (int, error) {
			src := r.topo.source
			if src == nil {
				src = noPosteriors{}
			}
			got, err := store.ApplyPromotions(ctx, src, d.Clock, topologyAuditor{bus: d.Bus})
			return got.RowsChanged, err
		},
	}
}

// publishReconciled publishes the one reconciled event of a changing pass.
func (r *FleetRuntime) publishReconciled(ctx context.Context, changed int) error {
	lanes, err := r.res.topo.ListLanes(ctx, false)
	if err != nil {
		return wrapKind(err, "fleet topology: count live lanes")
	}
	r.topo.generation++
	raw, err := json.Marshal(reconciledWire{SchemaVersion: 1, Generation: r.topo.generation, Lanes: len(lanes), Changed: changed})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "fleet topology: marshal the reconciled event")
	}
	if _, err := r.topo.deps.Bus.Publish(ctx, fleetTopologyNamespace, fleetTopologyReconciledKind, fleetTopologySource, raw); err != nil {
		return wrapKind(err, "fleet topology: publish the reconciled event")
	}
	return nil
}

// Health resolves HealthKeys over the live lanes, domains, accounts and
// buckets; any read error is (nil, err).
func (r *FleetRuntime) Health(ctx context.Context) (map[economics.HealthKey]economics.KeyHealth, error) {
	store := r.res.topo
	lanes, err := store.ListLanes(ctx, false)
	if err != nil {
		return nil, err
	}
	domains, err := store.ListQuotaDomains(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[topology.AccountID]topology.Account, len(accounts))
	for _, a := range accounts {
		byID[a.ID] = a
	}
	snap, err := topology.TakeQuotaSnapshot(ctx, r.res.quota, domains, r.topo.clock.Now())
	if err != nil {
		return nil, err
	}
	return economics.HealthKeys(snap, lanes, domains, byID, r.topo.clock), nil
}

// noPosteriors is the source used before SetPosteriorSource: expired
// reversals still restore, nothing is promoted.
type noPosteriors struct{}

func (noPosteriors) LanePosteriors(context.Context, topology.LaneID) ([]topology.Posterior, error) {
	return nil, nil
}

// topologyAuditor publishes each promotion event as JSON on the audit namespace.
type topologyAuditor struct{ bus *events.Bus }

// RecordPromotion publishes ev; a publish error is returned.
func (a topologyAuditor) RecordPromotion(ctx context.Context, ev topology.PromotionEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "fleet topology: marshal promotion event")
	}
	_, err = a.bus.Publish(ctx, fleetTopologyAuditNamespace, fleetTopologyPromotionKind, fleetTopologySource, raw)
	return err
}

// wrapKind wraps err keeping its Kind (KindInternal when it has none).
func wrapKind(err error, format string, args ...any) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindInternal
	}
	return cascade.Wrapf(kind, err, format, args...)
}
