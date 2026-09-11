// Purpose: `cascade node serve`'s presence-subsystem wiring: the real
//
//	R-16.37 probe loop (internal/nodes.Prober) and the R-16.67
//	network-change poller (internal/nodes.NetworkWatcher), over the SAME
//	RecordStore/KnownHosts node_serve.go's ServeRegistry already uses,
//	and a dedicated embedded event store this daemon owns.
//
// Inputs: nodeServeComposition (dataDir, keystore, self identity,
//
//	RecordStore, KnownHosts) and nodeServeDeps (Clock/GOOS), both built
//	once by composeNodeServe.
//
// Outputs: a running Prober + NetworkWatcher pair (goroutines, canceled
//
//	via ctx) and a closer for the event store they publish through; a
//	typed error if the store cannot be opened.
//
// Constraints: NEW STORE, NOT the main cascade daemon's — `node serve` is
//
//	its own process (node_serve.go's own doc comment), so this cannot
//	share internal/daemon's provider.Store; it opens a second, small,
//	embedded one at <dataDir>/nodes/events.db, mirroring
//	provider_health_cmd.go's openProviderStorage precedent
//	(runtime.OpenEmbeddedWriteStore), the one other cmd/cascade
//	composition root with the identical single-process/no-daemon-store
//	shape. No new ssh transport (RouteChecker dials through the exact
//	same nodes.NewSSHDialer node_admit.go's dialForHostKey already uses).
//
// SPORT: cmd/cascade/node (CHG — retires internal/build/
//
//	testonly-allow.json's UNOWNED internal/nodes.NewProber/
//	NewNetworkWatcher entries; this file is their real caller_site).
package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// presenceEventsDBFile is the node-serve process's own small event store,
// distinct from the main daemon's cascade.db (see package doc).
const presenceEventsDBFile = "events.db"

// startPresenceSubsystems opens comp's presence event store and starts a
// real Prober (routing through comp's RecordStore and a keystore-signed
// SSH RouteChecker) plus a real NetworkWatcher whose OnChange feeds the
// Prober's out-of-cycle trigger — exactly the pairing
// NetworkWatcherDeps.OnChange's own doc comment names. Returns a closer
// for the event store; the two loops themselves stop via ctx.
func startPresenceSubsystems(ctx context.Context, comp nodeServeComposition, deps nodeServeDeps) (func(), error) {
	dbPath := filepath.Join(comp.dataDir, "nodes", presenceEventsDBFile)
	// OpenEmbeddedWriteStore does NOT create the parent directory (verified
	// in internal/runtime/daemonless.go: it goes straight to sqlite.Open),
	// so this caller must. Omitting it passed locally and failed on every CI
	// platform, because a developer machine already has <data>/nodes from
	// earlier node work while a fresh runner does not — the sqlite schema
	// init then failed against a path whose directory did not exist. 0o700
	// matches daemon_unix.go's own MkdirAll for the log directory: this is
	// operator data under the private data dir, not world-readable output.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node serve: create presence event store directory")
	}
	store, err := cruntime.OpenEmbeddedWriteStore(ctx, dbPath, nil, nil)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "node serve: open presence event store")
	}

	bus := events.New(store, deps.Clock)
	probeTicker := deps.ProbeTicker
	if probeTicker == nil {
		probeTicker = nodes.NewSystemTicker(nodes.DefaultProbeInterval)
	}
	prober := nodes.NewProber(nodes.ProberDeps{
		Records:      comp.recordStore,
		Clock:        deps.Clock,
		Ticker:       probeTicker,
		RouteChecker: buildRouteChecker(comp, deps),
		Bus:          bus,
	})
	watcher := nodes.NewNetworkWatcher(nodes.NetworkWatcherDeps{
		Ticker:   nodes.NewSystemTicker(nodes.DefaultProbeInterval),
		Bus:      bus,
		OnChange: prober.TriggerOutOfCycle,
	})

	go prober.Run(ctx)
	go watcher.Run(ctx)

	return func() { _ = store.Close() }, nil
}

// buildRouteChecker returns the real, keystore-authenticated SSH
// RouteChecker — the same Dialer construction node_admit.go's
// dialForHostKey already uses (nodes.NewSSHDialer, authenticating as this
// node's OWN identity, per tunnel.go's keystoreSigner contract), so the
// prober's remote-via-route leg (R-16.37 §Nodes, gated at the third
// consecutive miss by prober.go's own probeOne) is real rather than the
// always-false defaultRouteChecker.
func buildRouteChecker(comp nodeServeComposition, deps nodeServeDeps) nodes.RouteChecker {
	dialer := nodes.NewSSHDialer(comp.keystore, comp.self.NodeID, comp.self.PubKey, 0)
	return nodes.NewSSHRouteChecker(comp.recordStore, dialer, comp.knownHosts, deps.GOOS)
}
