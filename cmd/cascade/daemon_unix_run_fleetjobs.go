//go:build !windows

// Purpose: buildRPCServer's fleet.*/node.*/job.*/lease.* call sites, split
//
//	out of daemon_unix_run.go under its 300-line file cap (mechanical
//	relocation, same composition-root concern that file's own doc
//	comment already names -- not a new concern).
//
// SPORT: cmd/cascade/daemon (CHANGED, P1-E40-W9-S77-T3 wiring split).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireFleetAndNodeHandlers is buildRPCServer's call site for four
// handlers that ship built, tested and otherwise unreachable, split out
// purely to keep buildRPCServer under Art.10.3's 50-line function cap:
// fleet.journal_show/replay (P1-E13-W3-S27-T4, over the SAME store
// wireResumeScan reads its journal from — a second reader, not a second
// journal), fleet.attention.list/get/ack (P1-E18-W4-S39-T1),
// node.upgrade (P1-E17-W4-S36-T5, see node_upgrade_rpc.go's header),
// fleet.quota.snapshot (P1-E40-W9-S77-T3, see
// internal/daemon/quota_rpc.go's header), and fleet.mode.show/set
// (P1-E41-W9-S79-T2, see internal/daemon/fleet_mode_rpc.go's header).
func wireFleetAndNodeHandlers(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, settings daemon.Settings) error {
	daemon.RegisterFleetJournalHandler(registry, store, clock)
	// sync.status/run/conflicts_list/conflicts_resolve (P1-E17-W4-S38-T3).
	// Mounted unconditionally: see internal/daemon/sync_rpc.go for why a
	// nil store does not disable this namespace the way it disables its
	// neighbours.
	daemon.RegisterSyncHandlers(registry, store, clock, settings.Sync)
	// ONE attention queue and ONE dispatch journal, built here and shared:
	// the attention handler serves the queue over RPC while the dispatch
	// recovery path writes into it, and the journal verb mounts the same
	// store the continuity reader replays. A second store over either
	// namespace would be a second view of one thing, and a held dispatch
	// filed into one would be invisible in the other (P1-E17-W4-S37-T6).
	attention := daemon.NewAttentionStore(store, clock, bus)
	dispatchJournal := journal.New(store, clock, "nodes.dispatch")
	daemon.RegisterFleetAttentionHandler(registry, store, clock, bus)
	// supervisor.snapshot/events_schema (P1-E18-W4-S40-T4), now over the
	// process's REAL metrics registry (P1-E18-W4-S40-T3 closed the
	// C-S05.T4 half of the gap this comment used to record): the fleet
	// counters are registered against the same registry this hands the
	// handler, so a snapshot reads the numbers the supervision stages
	// actually incremented, without importing internal/fleet.
	//
	// autonomy is still nil — no *policy.Controller is threaded into this
	// function — so the autonomy reading remains the honestly-degraded
	// "locked"/"none" internal/daemon/supervisor_rpc.go's DISCLOSED GAPS
	// note describes. A metrics registry that could not be built is an
	// error rather than a silent nil: zeros that read like a quiet fleet
	// are the one answer nobody can act on.
	metricsReg, _, err := daemonMetrics()
	if err != nil {
		return err
	}
	daemon.RegisterSupervisorHandler(registry, store, clock, bus, metricsReg, nil)
	daemon.RegisterNodeUpgradeHandler(registry, paths, clock)
	// node.dispatch + the two verbs the NODE calls back over its own
	// tunnel (P1-E17-W4-S37-T2, see internal/daemon/node_dispatch_rpc.go's
	// header). The rendezvous is returned but not held here: the node
	// side reaches it through the registry, which is the only path a
	// dispatch report can arrive by.
	dispatcher, _ := daemon.RegisterNodeDispatchHandlers(
		registry,
		nodes.NewRecordStore(nodes.NewFileRecordBackend(paths.DataDir()), clock),
		clock,
		func() (nodes.Section, error) { return settings.Nodes, nil },
		daemon.RecoveryStores{Journal: dispatchJournal, Attention: attention},
	)
	// The journal-stream verb rides the SAME fencing register, so a
	// streamed record is fenced against the very attempt the ship leg
	// minted for it.
	daemon.RegisterNodeDispatchJournal(registry, dispatcher, dispatchJournal)
	// The returned *sql.DB is intentionally not closed here: this
	// composition root does not yet track per-registration close hooks
	// (registerContextEngineHandlers' own connections have the identical
	// disclosed gap) -- it lives for the daemon process's lifetime and is
	// reclaimed on process exit, matching every sibling registerXHandler
	// call in this function today.
	if _, err := daemon.RegisterFleetQuotaHandler(registry, paths, clock); err != nil {
		return err
	}
	// fleet.mode.show/set (P1-E41-W9-S79-T2, see
	// internal/daemon/fleet_mode_rpc.go's header).
	_, modeErr := daemon.RegisterFleetModeHandler(registry, paths, clock)
	return modeErr
}

// wireFleetNodeAndJobHandlers combines wireFleetAndNodeHandlers and
// wireJobRPCHandlers behind one error check, purely to keep buildRPCServer
// under Art.10.3's 50-line function cap -- mechanical composition, not a
// new concern.
func wireFleetNodeAndJobHandlers(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, settings daemon.Settings) error {
	if ferr := wireFleetAndNodeHandlers(registry, store, clock, bus, paths, settings); ferr != nil {
		return ferr
	}
	return wireJobRPCHandlers(registry, paths, clock)
}

// wireJobRPCHandlers is buildRPCServer's job.*/lease.* call site (see
// daemon_unix_jobs_rpc.go's header comment), split out purely to keep
// buildRPCServer under Art.10.3's 50-line function cap.
//
// This daemon's composition root has no H/S-16.T3 trust-store wiring at
// HEAD, so rpc.MapTrustStore{} is a genuinely EMPTY store: no pubkey
// fingerprint is ever enrolled, so lease.release's elevated (releasing
// another job's lease) path always fails closed with a typed KindNotFound
// from VerifyAttestation — never a nil pointer, and never a silent
// success. lease.release's UNelevated (own-lease) path is unaffected and
// works today. Replace rpc.MapTrustStore{} once a real trust store is
// wired at this composition root.
func wireJobRPCHandlers(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) error {
	_, err := wireJobRPC(context.Background(), registry, paths, clock, rpc.NewNonceLedger(clock), rpc.MapTrustStore{})
	return err
}
