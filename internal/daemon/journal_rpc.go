package daemon

// Purpose: T0-daemon-composition-root — registers fleet.journal_show and
//   fleet.journal_replay (P1-E13-W3-S27-T4) on the daemon's RPC router
//   over a real internal/fleet/journal.SQLiteStore, closing the gap
//   R-14.223 recorded: internal/fleet/journal.RegisterHandlers had a test
//   caller only, and its own package doc named cmd/cascade/
//   daemon_unix_run.go (the unplanned root) as the only concrete caller
//   idea on record. This file puts the actual construction in
//   internal/daemon instead — the PLANNED composition root R-14.223
//   named — mirroring recall_index.go's and context_scope.go's own
//   registerXHandler precedent: the composition logic lives in this
//   package, cmd/cascade only calls it with the store/clock it already
//   has open.
// Inputs: the daemon's shared *rpc.Registry, the already-open
//   provider.Store (cmd/cascade/daemon_unix_store.go's openRuntimeStore)
//   and runtime.Clock buildRPCServer already threads into every sibling
//   registerXHandler.
// Outputs: fleet.journal_show/replay bound to a real
//   journal.SQLiteStore over journal.DefaultNamespace — the SAME
//   namespace cmd/cascade/daemon_resume.go's wireResumeScan already
//   reads in production, so this is the second production reader over
//   the one real journal, not a second journal.
// Constraints: journal.RegisterHandlers takes no error return (it only
//   calls registry.Register, which cannot fail), so there is nothing for
//   a Manifest fail-loud entry to report here; R-14.87's fail-loud rule
//   applies to subsystems that can fail to bind/start, and this
//   registration cannot. A nil store disables the namespace entirely
//   (registerRecallHandler/registerMemoryHandler's existing nil-store
//   degradation), rather than constructing a store over a store that
//   does not exist.
// SPORT: internal/daemon (ADD, T0-daemon-composition-root).

import (
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// RegisterFleetJournalHandler mounts fleet.journal_show and
// fleet.journal_replay on registry over a real journal.SQLiteStore built
// from store/clock. A nil store registers nothing, matching this file's
// sibling registerXHandler functions' documented degradation.
func RegisterFleetJournalHandler(registry *rpc.Registry, store provider.Store, clock runtime.Clock) {
	if store == nil {
		return
	}
	reader := journal.New(store, clock, journal.DefaultNamespace)
	journal.RegisterHandlers(registry, reader)
}
