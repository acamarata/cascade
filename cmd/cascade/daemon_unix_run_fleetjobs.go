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
// node.upgrade (P1-E17-W4-S36-T5, see node_upgrade_rpc.go's header), and
// fleet.quota.snapshot (P1-E40-W9-S77-T3, see
// internal/daemon/quota_rpc.go's header).
func wireFleetAndNodeHandlers(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider) error {
	daemon.RegisterFleetJournalHandler(registry, store, clock)
	daemon.RegisterFleetAttentionHandler(registry, store, clock, bus)
	daemon.RegisterNodeUpgradeHandler(registry, paths, clock)
	// The returned *sql.DB is intentionally not closed here: this
	// composition root does not yet track per-registration close hooks
	// (registerContextEngineHandlers' own connections have the identical
	// disclosed gap) -- it lives for the daemon process's lifetime and is
	// reclaimed on process exit, matching every sibling registerXHandler
	// call in this function today.
	_, err := daemon.RegisterFleetQuotaHandler(registry, paths, clock)
	return err
}

// wireFleetNodeAndJobHandlers combines wireFleetAndNodeHandlers and
// wireJobRPCHandlers behind one error check, purely to keep buildRPCServer
// under Art.10.3's 50-line function cap -- mechanical composition, not a
// new concern.
func wireFleetNodeAndJobHandlers(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider) error {
	if err := wireFleetAndNodeHandlers(registry, store, clock, bus, paths); err != nil {
		return err
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
