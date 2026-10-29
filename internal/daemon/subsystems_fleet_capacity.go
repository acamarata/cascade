package daemon

// Purpose: the fleet runtime's capacity step: the production Compositor (providers,
// nodes, sampler) on a supervised refresh loop, the ONE capacity.Fleet over the learn
// adapters, the interim TierPolicy selector, registry-lane domains and node placement,
// and the periodic learn aggregation. Outputs: Fleet(), Compositor(), CapacityParts(),
// fleet.capacity_changed on the bus. Constraints: no second Fleet or Compositor; no fleet.lease
// RPC. Deviation: providers.db gets its own handle (the registry reads it) and nodes a RecordStore.
// SPORT: internal/daemon (ADD, P1-CAP-05).

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The capacity step's manifest rows, cadences and fixed names.
const (
	fleetCapacityRefreshSubsystem = "fleet-capacity-refresh"
	fleetCapacityRefreshInterval  = 2 * time.Second
	fleetLearnAggregateSubsystem  = "fleet-learn-aggregate"
	fleetLearnAggregateInterval   = time.Hour
	fleetCapacityTTL              = 15 * fleetCapacityRefreshInterval
	localControllerNodeID         = "controller"
)

var newCapacityTicker = func(period time.Duration) runtime.Ticker { return runtime.NewSystemTicker(period) }
var newAggregateTicker = newCapacityTicker // ticker seams for the two loops

// afterRegistryDBOpen is a test seam: the window between opening providers.db and registering its closer.
var afterRegistryDBOpen = func(*FleetRuntime) {}

// FleetCapacityParts is the capacity step's composed graph, read-only.
type FleetCapacityParts struct {
	Compositor       *capacity.Compositor
	Registry         *registry.Registry
	Nodes            *nodes.RecordStore
	Scorer           *learn.SQLiteCapabilityScorer
	Estimates        *learn.SQLiteEstimateSource
	Policy           *capacity.TierPolicy
	Domains          RegistryLaneDomains
	Placer           NodePlacer
	ControllerNodeID string
	SaltPath         string
	Deps             capacity.FleetDeps
}

type fleetCapacity struct {
	parts    FleetCapacityParts
	fleet    *capacity.Fleet
	sampler  *governor.Sampler
	stop     chan struct{}
	logger   *slog.Logger
	agents   atomic.Pointer[func() []capacity.AgentLane] // SetAgentLanes source; nil means none
	nodesSig string                                      // refresh's last applied node-record signature
	nodesAt  time.Time
	selfID   string // the controller's own node id
	clock    runtime.Clock
	tunnels  atomic.Pointer[nodes.TunnelStateLookup] // SetNodeTunnels source; nil means no node is connected
}

// Fleet returns the one capacity.Fleet: the in-process lease door.
func (r *FleetRuntime) Fleet() *capacity.Fleet { return r.capa.fleet }

// Compositor returns the one compositor behind fleet.capacity.
func (r *FleetRuntime) Compositor() *capacity.Compositor { return r.capa.parts.Compositor }

// SetAgentLanes registers the agent-lane source merged into the Fleet's snapshot; nil clears it.
func (r *FleetRuntime) SetAgentLanes(fn func() []capacity.AgentLane) {
	var p *func() []capacity.AgentLane
	if fn != nil {
		p = &fn
	}
	r.capa.agents.Store(p)
}

// SetNodeTunnels registers the live tunnel lookup node placement needs; nil (the default) reports
// no node connected, so a placement need refuses (fail closed). Wiring it is P1-TOP-04's.
func (r *FleetRuntime) SetNodeTunnels(fn nodes.TunnelStateLookup) {
	if fn == nil {
		r.capa.tunnels.Store(nil)
		return
	}
	r.capa.tunnels.Store(&fn)
}

// CapacityParts returns the composed graph.
func (r *FleetRuntime) CapacityParts() FleetCapacityParts { return r.capa.parts }

// buildCapacity is the capacity step; its closer is registered before anything can fail.
func (r *FleetRuntime) buildCapacity(ctx context.Context, d FleetRuntimeDeps) error {
	regDB, err := openRegistryDB(ctx, d.Paths, d.Clock)
	if err != nil {
		return err
	}
	afterRegistryDBOpen(r)
	stop := make(chan struct{})
	closeAll := func() error { close(stop); return regDB.Close() }
	if err := r.registerOrFail(closeAll); err != nil { // Close already ran: closeAll ran at once, build nothing
		return err
	}
	c := &r.capa
	*c = fleetCapacity{sampler: r.sampler, stop: stop, logger: d.Logger, clock: d.Clock}
	if err := c.buildLearn(ctx, d, r.res.db); err != nil {
		return err
	}
	controller, self, err := controllerNodeIDs(d.Binding)
	if err != nil {
		return err
	}
	c.selfID = self
	p := &c.parts
	p.Registry, p.ControllerNodeID = registry.NewRegistry(regDB, d.Clock), controller
	p.Nodes = nodes.NewRecordStore(nodes.NewFileRecordBackend(d.Paths.DataDir()), d.Clock)
	p.Compositor = capacity.NewCompositor(d.Clock, fleetCapacityTTL, self)
	capacity.WireSSE(p.Compositor, d.Bus)
	p.Policy = capacity.NewTierPolicy(p.Estimates, capacity.NewDefaultProbeAdmission(), d.Clock)
	p.Domains = RegistryLaneDomains{Topology: r.res.topo}
	p.Placer = NodePlacer{Records: p.Nodes, Tunnels: func() nodes.TunnelStateLookup {
		if fn := c.tunnels.Load(); fn != nil {
			return *fn
		}
		return nil
	}}
	if err := c.buildFleet(d, r); err != nil {
		return err
	}
	c.refresh(ctx)
	d.Manifest.GoSupervised(ctx, fleetCapacityRefreshSubsystem, "every "+fleetCapacityRefreshInterval.String(),
		c.every(newCapacityTicker(fleetCapacityRefreshInterval), c.refresh))
	agg := learn.NewAggregator(r.res.db, d.Clock, p.SaltPath)
	aggregate := func(ctx context.Context) { _, err := agg.Aggregate(ctx); c.warn("fleet learn aggregation", err) }
	loop := c.every(newAggregateTicker(fleetLearnAggregateInterval), aggregate)
	d.Manifest.GoSupervised(ctx, fleetLearnAggregateSubsystem, "every "+fleetLearnAggregateInterval.String(),
		func(ctx context.Context) error { aggregate(ctx); return loop(ctx) }) // first pass at start: a daemon restarted more often than hourly would never aggregate
	return nil
}

// buildLearn applies the learn schema on db, ensures the salt, builds the scorer and estimates.
func (c *fleetCapacity) buildLearn(ctx context.Context, d FleetRuntimeDeps, db *sql.DB) error {
	if err := learn.ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, d.Clock); err != nil {
		return err
	}
	c.parts.SaltPath = filepath.Join(d.Paths.DataDir(), "learn", "aggregate_salt")
	if _, err := learn.EnsureAggregateSalt(c.parts.SaltPath); err != nil {
		return err
	}
	c.parts.Scorer = learn.NewSQLiteCapabilityScorer(db, d.Clock)
	c.parts.Estimates = learn.NewSQLiteEstimateSource(db)
	return nil
}

// buildFleet assembles FleetDeps and the one Fleet; [conductor] restricted_external_lanes is read once, here.
func (c *fleetCapacity) buildFleet(d FleetRuntimeDeps, r *FleetRuntime) error {
	section, _ := d.Config.Extra["conductor"].(map[string]interface{})
	permits, err := conductor.ParseRestrictedPermits(section)
	if err != nil {
		return err
	}
	p := &c.parts
	p.Deps = capacity.FleetDeps{
		Snapshot: func(context.Context) (capacity.FleetSnapshot, error) { return c.snapshot(d.Clock) },
		Selector: capacity.NewTierPolicySelector(p.Policy), Scorer: p.Scorer.For, Reserver: r.res.reserver, Reservations: r.res.resStore,
		Jobs: r.res.jobs, Decisions: LearnDecisionRecorder{Writer: learn.SQLiteSchedulerDecisionWriter{}}, DomainOf: p.Domains.DomainOf, Place: p.Placer.Place,
		ControllerNodeID: p.ControllerNodeID, RestrictedPermits: permits, Clock: d.Clock,
		TTL: time.Duration(jobs.DefaultLeaseDefaults().TTLSeconds) * time.Second,
	}
	c.fleet, err = capacity.NewFleet(p.Deps)
	return err
}

// every runs fn on each tick until the run context ends or the runtime
// closes (the step's closer closes stop), stopping t on the way out.
func (c *fleetCapacity) every(t runtime.Ticker, fn func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-c.stop:
				return nil
			case <-t.C():
				fn(ctx)
			}
		}
	}
}

// warn logs err under msg when there is one; the loops retry on their next tick.
func (c *fleetCapacity) warn(msg string, err error) {
	if err != nil && c.logger != nil {
		c.logger.Warn(msg, slog.String("error", err.Error()))
	}
}

// snapshot is the Fleet's snapshot source: the compositor's snapshot plus the
// registered agent lanes as available tier-zero slots.
func (c *fleetCapacity) snapshot(clock runtime.Clock) (capacity.FleetSnapshot, error) {
	snap := c.parts.Compositor.Snapshot()
	fn := c.agents.Load()
	if fn == nil {
		return snap, nil
	}
	slots, err := capacity.AgentLaneSlots((*fn)(), clock.Now())
	if err != nil {
		return capacity.FleetSnapshot{}, err
	}
	for id, slot := range slots {
		snap.Lanes[conductor.LaneID(id)] = slot
	}
	return snap, nil
}

// refresh reads providers, node records and the latest admission sample into the
// compositor; a failed read keeps the last slots (they age to unknown) and is logged.
// UpdateNodes stamps each node slot (they expire after the TTL) and replaces them all, so the
// list carries the controller itself (it has no record): no snapshot ever lacks the self slot.
// An unchanged list is re-applied only every third of the TTL.
func (c *fleetCapacity) refresh(ctx context.Context) {
	p := &c.parts
	err := p.Compositor.UpdateProviders(ctx, p.Registry)
	recs, nerr := p.Nodes.List()
	now := c.clock.Now()
	if sig := nodesSignature(recs); nerr == nil && (c.nodesAt.IsZero() || sig != c.nodesSig || now.Sub(c.nodesAt) >= fleetCapacityTTL/3) {
		if nerr = p.Compositor.UpdateNodes(ctx, withSelf(recs, c.selfID)); nerr == nil {
			c.nodesSig, c.nodesAt = sig, now
		}
	}
	p.Compositor.UpdateSampler(c.sampler.Snapshot())
	c.warn("fleet capacity refresh", errors.Join(err, nerr))
}

// openRegistryDB opens providers.db (the data dir exists: the reservation step made it), migrated.
func openRegistryDB(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	path := filepath.Join(paths.DataDir(), "providers.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "fleet runtime: open %s", path)
	}
	db.SetMaxOpenConns(1)
	if err := registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// controllerNodeIDs reads the binding once: a bound node names its controller by the
// endpoint and itself by its enrollment id; an unbound daemon is the controller.
func controllerNodeIDs(b nodes.ControllerBindingBackend) (controller, self string, err error) {
	binding, ok, err := b.Load()
	if err != nil {
		return "", "", cascade.Wrap(cascade.KindUnavailable, err, "fleet runtime: read the controller binding")
	}
	if ok && binding.Endpoint != "" {
		return binding.Endpoint, binding.EnrollmentID, nil
	}
	return localControllerNodeID, localControllerNodeID, nil
}
