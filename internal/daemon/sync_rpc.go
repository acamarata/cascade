package daemon

// Purpose: the daemon composition root's sync.* registration — the four
//   methods of internal/sync's RPC surface (P1-E17-W4-S38-T3), mounted on
//   the shared registry over the SAME cursor store every other
//   registerXHandler in this package is handed.
// Inputs: the daemon's *rpc.Registry, the already-open provider.Store and
//   runtime.Clock.
// Outputs: sync.status, sync.run, sync.conflicts_list and
//   sync.conflicts_resolve bound to a real internal/sync.Engine.
// Constraints:
//   - A NIL STORE STILL REGISTERS, unlike this package's other
//     registerXHandler functions. Their namespaces are entirely a store's
//     contents; sync's are not. The conflict journal is the engine's own
//     in-process record and is perfectly readable without a database, so
//     making `sync.status` an unknown method because no store opened would
//     hide a report that works. What a missing store costs is the cursor
//     POSITIONS, and status already reports those as unread rather than as
//     zero.
//   - Elevation is enforced by internal/rpc's shared ElevationMiddleware
//     over the canonical elevationTable, exactly as every sibling
//     registerXHandler leaves it. The handler-level gate below therefore
//     does not re-ask; it records that the transport already did.
// SPORT: internal/daemon sync.* (ADD) — P1-E17-W4-S38-T3.

import (
	"context"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/provider"
)

// transportElevationGate satisfies internal/sync's ElevationGate for a
// handler reached through the RPC transport.
//
// It is NOT a bypass. internal/sync refuses an elevated resolution when no
// gate is wired, because a machine that cannot check must not be the one
// that allows the server's copy to be discarded — and on this path the
// check has already happened: ElevationMiddleware demanded and verified an
// attestation for sync.conflicts_resolve before this handler was called.
// Wiring nothing here would refuse every properly attested request; wiring
// a second prompt would ask an already-authorised caller twice.
type transportElevationGate struct{}

func (transportElevationGate) Authorize(context.Context, string) error { return nil }

// RegisterSyncHandlers mounts the sync.* methods on registry.
//
// PeerTier is the controller's: the tier a status report answers
// eligibility for is the tier of the peer the question is about, and a
// question asked of this daemon is a question about this machine.
func RegisterSyncHandlers(registry *rpc.Registry, store provider.Store, clock runtime.Clock, cfg syncpkg.Config) {
	engine := syncpkg.NewEngine(store, clock, nil)
	syncpkg.RegisterHandlers(registry, syncpkg.Deps{
		Engine:   engine,
		PeerTier: nodes.TierController,
		Config:   cfg,
		Gate:     transportElevationGate{},
		// Run stays nil until a sync session transport exists
		// (P1-E17-W4-S38-T5, which needs a second machine). sync.run
		// therefore REPORTS that nothing is wired rather than reporting a
		// sync that never happened.
	})
}
