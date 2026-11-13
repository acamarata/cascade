//go:build !windows

// Purpose: registers the fleet-topology daemon wiring: the fleet runtime's
// topology pass is bound once over the runtime's one provider registry, the
// agent lanes registered through setFleetAgentLanes and provider-usage.db;
// one ReconcileNow runs before serve, then one supervised pass every 60 s.
// Constraints: no second registry open or topology store. An invalid lane
// pin refuses start before any write; any other pre-serve error fails the
// manifest entry and the daemon keeps serving with no rows (leases refuse
// ErrNoQuotaDomain). Only the first pass blocks the socket; later passes and
// the event relay stop with w.Ctx. No row is deleted here.
// SPORT: cmd/cascade daemon registrations (P1-TOP-06f).
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The manifest names of the pre-serve pass and of the periodic loop.
const (
	fleetTopologySubsystem     = "fleet-topology"
	fleetTopologyPassSubsystem = "fleet-topology-pass"
	fleetTopologyInterval      = 60 * time.Second
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: fleetTopologySubsystem, Phase: phaseFleet, Order: 60,
	Wire: wireFleetTopology,
})

// topologyRegistry returns the fleet runtime's one provider registry; a
// runtime without one is KindUnavailable. Tests replace it to inject an
// unreadable registry.
var topologyRegistry = func(fleet *daemon.FleetRuntime) (*registry.Registry, error) {
	reg := fleet.CapacityParts().Registry
	if reg == nil {
		return nil, cascade.New(cascade.KindUnavailable, "fleet topology: the fleet runtime has no provider registry")
	}
	return reg, nil
}

// newTopologyTicker builds the periodic pass ticker; tests replace it.
var newTopologyTicker = func(d time.Duration) runtime.Ticker { return runtime.NewSystemTicker(d) }

// agentLanePrefix marks the lanes the SetAgentLanes source adds to the
// fleet snapshot (capacity.AgentLaneSlots).
const agentLanePrefix = "agent:"

// topologyAgentLanes returns the pass's AgentLanes: the agent lanes the
// runtime's SetAgentLanes source (P1-AGT-08) merges into the fleet snapshot,
// read on every pass. A snapshot read error is never read as absence: the
// last good list is kept, so no agent lane is retired by a failed read.
func topologyAgentLanes(ctx context.Context, fleet *daemon.FleetRuntime) func() []topology.AgentLaneRecord {
	var mu sync.Mutex
	var last []topology.AgentLaneRecord
	return func() []topology.AgentLaneRecord {
		mu.Lock()
		defer mu.Unlock()
		snapshot := fleet.CapacityParts().Deps.Snapshot
		if snapshot == nil {
			return last
		}
		snap, err := snapshot(ctx)
		if err != nil {
			return last
		}
		out := make([]topology.AgentLaneRecord, 0, len(snap.Lanes))
		for id, slot := range snap.Lanes {
			if strings.HasPrefix(string(id), agentLanePrefix) && slot.ProfileRef != "" {
				out = append(out, topology.AgentLaneRecord{LaneName: string(id), Harness: slot.ProfileRef})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].LaneName < out[j].LaneName })
		last = out
		return out
	}
}

// wireFleetTopology binds the pass, runs it once and starts the loop.
func wireFleetTopology(w *daemonWiring) error {
	fleet := w.Deps.Fleet
	if fleet == nil || w.Paths == nil {
		return nil // a test-built wiring without a fleet runtime
	}
	if _, err := runtime.ParseLaneReversalPins[topology.LaneClass](w.Deps.Config, nil); err != nil {
		return err // an invalid pin refuses daemon start before any write
	}
	reg, err := topologyRegistry(fleet)
	if err != nil {
		w.Manifest.Failed(fleetTopologySubsystem, err.Error())
		return nil
	}
	db, relay, err := openTopologyInputs(w)
	if err != nil {
		w.Manifest.Failed(fleetTopologySubsystem, err.Error())
		return nil
	}
	reader := usage.NewReader(usage.NewManager(db, w.Clock))
	if err := fleet.BindTopologyPass(daemon.TopologyPassDeps{Registry: reg, AgentLanes: topologyAgentLanes(w.Ctx, fleet),
		Usage: reader, QuotaWindows: reader.QuotaWindows, Bus: relay, Clock: w.Clock}); err != nil {
		_ = db.Close()
		return err
	}
	first := true
	if rep, err := fleet.ReconcileNow(w.Ctx); err != nil {
		first = false
		w.Manifest.Failed(fleetTopologySubsystem, err.Error())
	} else {
		w.Manifest.Started(fleetTopologySubsystem, topologyDetail(rep))
	}
	w.Manifest.GoSupervised(w.Ctx, fleetTopologyPassSubsystem, "every "+fleetTopologyInterval.String(),
		func(ctx context.Context) error {
			defer func() { _ = db.Close() }()
			if first {
				runTopologyLoop(ctx, fleet, w.Logger)
			} else {
				<-ctx.Done()
			}
			return nil
		})
	return nil
}

// openTopologyInputs opens provider-usage.db and starts the pass's event relay.
func openTopologyInputs(w *daemonWiring) (*sql.DB, *events.Bus, error) {
	db, err := openMigratedDB(w.Ctx, filepath.Join(w.Paths.DataDir(), providerUsageDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, w.Clock, "", "")
		})
	if err != nil {
		return nil, nil, err
	}
	relay, err := startTopologyRelay(w)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, relay, nil
}

// runTopologyLoop runs one pass on every tick until ctx ends; a pass error
// is logged and the next tick retries.
func runTopologyLoop(ctx context.Context, fleet *daemon.FleetRuntime, logger *slog.Logger) {
	t := newTopologyTicker(fleetTopologyInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
		if _, err := fleet.ReconcileNow(ctx); err != nil && logger != nil && ctx.Err() == nil {
			logger.Warn("fleet topology pass failed", slog.String("error", err.Error()))
		}
	}
}

// topologyDetail is the manifest detail of a finished pass.
func topologyDetail(rep topology.Report) string {
	return "rows written " + strconv.Itoa(rep.RowsWritten)
}

// The pass's bus namespaces and the relay's manifest entry and cursor.
var topologyRelayNamespaces = [2]string{"fleet.topology", "fleet.topology.audit"}

const fleetTopologyRelaySubsystem = "fleet-topology-relay"

// startTopologyRelay returns the pass's bus: an in-memory bus whose events a
// supervised loop republishes, in order, on the daemon bus. The pass audits
// a reversal inside its cascade.db transaction, so a publish straight onto
// the daemon bus (also cascade.db) would wait on that transaction's own
// write lock; the relay publishes once the transaction has released it.
func startTopologyRelay(w *daemonWiring) (*events.Bus, error) {
	relay := events.New(&relayStore{}, w.Clock)
	var subs [len(topologyRelayNamespaces)]*events.Subscription
	for i, ns := range topologyRelayNamespaces {
		sub, err := relay.Subscribe(w.Ctx, ns, fleetTopologyRelaySubsystem, 64)
		if err != nil {
			_ = relay.Close()
			return nil, err
		}
		subs[i] = sub
	}
	w.Manifest.GoSupervised(w.Ctx, fleetTopologyRelaySubsystem, "to the daemon bus", func(ctx context.Context) error {
		defer func() { _ = relay.Close() }()
		for {
			var ev events.Event
			ok, ns := false, topologyRelayNamespaces[0]
			select {
			case <-ctx.Done():
				return nil
			case ev, ok = <-subs[0].Events:
			case ev, ok = <-subs[1].Events:
				ns = topologyRelayNamespaces[1]
			}
			if !ok {
				return cascade.New(cascade.KindUnavailable, "fleet topology relay: a subscription stopped")
			}
			if _, err := w.Bus.Publish(ctx, ns, ev.Kind, ev.Source, ev.Payload); err != nil && w.Logger != nil {
				w.Logger.Warn("fleet topology relay", slog.String("namespace", ns), slog.String("error", err.Error()))
			}
		}
	})
	return relay, nil
}

// relayStore is the relay bus's in-memory provider.Store: a Scan hands each
// event record out once and drops it, so the relay holds only undelivered
// events. Tx is unused by the bus and refused.
type relayStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *relayStore) Get(_ context.Context, ns, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ns+"\x00"+key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "fleet topology relay: %s/%s not found", ns, key)
	}
	return v, nil
}

func (s *relayStore) Put(_ context.Context, ns, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[ns+"\x00"+key] = append([]byte(nil), value...)
	return nil
}

func (s *relayStore) Delete(_ context.Context, ns, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ns+"\x00"+key)
	return nil
}

func (s *relayStore) Scan(_ context.Context, ns, prefix string) (provider.Iterator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := &relayIter{i: -1}
	for k, v := range s.m {
		if key, ok := strings.CutPrefix(k, ns+"\x00"); ok && strings.HasPrefix(key, prefix) {
			it.keys, it.vals = append(it.keys, key), append(it.vals, v)
			delete(s.m, k)
		}
	}
	sort.Sort(it)
	return it, nil
}

func (s *relayStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return cascade.New(cascade.KindUnavailable, "fleet topology relay: no transactions")
}

// relayIter walks one Scan's records in key order.
type relayIter struct {
	keys []string
	vals [][]byte
	i    int
}

func (it *relayIter) Len() int           { return len(it.keys) }
func (it *relayIter) Less(a, b int) bool { return it.keys[a] < it.keys[b] }
func (it *relayIter) Swap(a, b int) {
	it.keys[a], it.keys[b] = it.keys[b], it.keys[a]
	it.vals[a], it.vals[b] = it.vals[b], it.vals[a]
}
func (it *relayIter) Next(context.Context) bool { it.i++; return it.i < len(it.keys) }
func (it *relayIter) Key() string               { return it.keys[it.i] }
func (it *relayIter) Value() []byte             { return it.vals[it.i] }
func (it *relayIter) Err() error                { return nil }
func (it *relayIter) Close() error              { return nil }
