//go:build !windows

// Purpose: composes the provider health scheduler into the daemon
//
//	(P1-GWY-09). One health.Manager is built over the daemon's providers
//	registry and event bus, handed to the dispatch resolver (its
//	consecutive-transient counter demotes through it) and to the
//	health.Scheduler, which probes degraded and dead providers on the
//	R-14.33 schedule so a recovered provider returns without operator
//	action.
//
// Inputs: the daemon run context, its Manifest, the providers registry,
//
//	the event bus, the clock, providerDeps (probe transport seams) and the
//	admitted gateway accounts with their token source.
//
// Outputs: the shared *health.Manager; the scheduler goroutine, tracked as
//
//	the "provider-health" subsystem; and the retry loop's lane-eligibility
//	seam.
//
// Constraints: the scheduler runs on the run context through
//
//	Manifest.GoSupervised, so it ends with the context and is joined by
//	Manifest.Wait before the stores close. Probes go through the process
//	egress gate (egressEngineForHealth), which the caller binds first.
//
// SPORT: cmd/cascade daemon provider_health/ADD (P1-GWY-09).
package main

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/health"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// providerHealthSubsystem is the Manifest name of the scheduler goroutine.
const providerHealthSubsystem = "provider-health"

// providerHealthScanEvery is how often the scheduler looks for due probes.
const providerHealthScanEvery = 30 * time.Second

// startProviderHealth builds the one Manager and starts the scheduler on
// run under manifest. It fails closed on a missing collaborator rather
// than starting a scheduler that probes nothing.
func startProviderHealth(run context.Context, manifest *daemon.Manifest, reg *providerregistry.Registry, bus *events.Bus, clock runtime.Clock, deps providerDeps, gw gatewayProbeConfig) (*health.Manager, error) {
	if run == nil || manifest == nil || reg == nil || bus == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "provider health: run context, manifest, registry, bus and clock are required")
	}
	mgr := health.NewManager(reg, bus, clock, 0).WithEgress(egressEngineForHealth(), newRoutedProber(deps, gw))
	sched := health.NewScheduler(mgr, reg, clock, health.DefaultIntervals())
	manifest.GoSupervised(run, providerHealthSubsystem, "probing degraded and dead providers", func(ctx context.Context) error {
		sched.Run(ctx, providerHealthScanEvery)
		return nil
	})
	return mgr, nil
}

// startDaemonProviderHealth is the conductor wiring's call: the Manager over
// the daemon store's event bus, probing gateway records through the accounts
// admitted in cfg (the same set the resolver is built over).
func startDaemonProviderHealth(run context.Context, manifest *daemon.Manifest, reg *providerregistry.Registry, store provider.Store, clock runtime.Clock, cfg *runtime.Config) (*health.Manager, error) {
	accounts, src, err := daemonGateways(cfg, nil)
	if err != nil {
		return nil, err
	}
	return startProviderHealth(run, manifest, reg, events.New(store, clock), clock, productionProviderDeps(), gatewayProbeConfig{Accounts: accounts, Source: src})
}

// The gateway-snapshot registration publishes the daemon's start-snapshot
// reader (w.Deps.Daemon.Getenv, which composeDaemon sets to
// credentialEnv.Getenv) before "conductor" (Order 10) builds the resolver and
// the health prober, and withdraws it when the run ends. The scrub is
// process-wide (it unsets the process environment), so the snapshot is too.
var _ = registerDaemonWiring(daemonRegistration{
	Name: "gateway-snapshot", Phase: phaseConductor, Order: 5,
	Wire: func(w *daemonWiring) error {
		if w.Deps.Daemon.Getenv == nil {
			return nil
		}
		entry := &snapshotEntry{getenv: w.Deps.Daemon.Getenv}
		snapshotReader.mu.Lock()
		snapshotReader.cur = entry
		snapshotReader.mu.Unlock()
		context.AfterFunc(w.Ctx, func() {
			snapshotReader.mu.Lock()
			defer snapshotReader.mu.Unlock()
			if snapshotReader.cur == entry {
				snapshotReader.cur = nil
			}
		})
		return nil
	},
})

// snapshotEntry is one daemon run's published start-snapshot reader.
type snapshotEntry struct{ getenv func(string) string }

// snapshotReader is the reader of the daemon running in this process.
var snapshotReader struct {
	mu  sync.Mutex
	cur *snapshotEntry
}

// daemonSnapshotGetenv returns the published start-snapshot reader, or an
// empty snapshot's reader when no daemon published one (a bare wiring test):
// nothing scrubbed, so every name falls through to the process environment.
func daemonSnapshotGetenv() func(string) string {
	snapshotReader.mu.Lock()
	defer snapshotReader.mu.Unlock()
	if snapshotReader.cur != nil {
		return snapshotReader.cur.getenv
	}
	return (&credentialEnv{}).Getenv
}

// laneEligibleByHealth is the retry loop's eligibility seam: a lane may
// take another attempt while its provider is healthy or unknown. A lane or
// health read that fails stays eligible, so a registry fault never turns
// into a refusal to try.
func laneEligibleByHealth(reg *providerregistry.Registry, mgr *health.Manager) conductor.LaneEligible {
	return func(ctx context.Context, lane conductor.LaneID) bool {
		lanes, err := reg.ListLanes(ctx)
		if err != nil {
			return true
		}
		for _, l := range lanes {
			if l.LaneName != string(lane) {
				continue
			}
			st, herr := mgr.GetHealth(ctx, l.ProviderName)
			return herr != nil || st == provider.HealthUnknown || st == provider.HealthHealthy
		}
		return true
	}
}
