//go:build !windows

// Purpose: the memory.* and recall.* registration pair, split out of
// daemon_unix_run.go so that file stays under Art.10.3's 300-line cap —
// the same split daemon_unix_store.go and daemon_unix_conductor.go
// already apply to the same composition root for the same reason.
//
// SPORT: cmd/cascade daemon composition (CHANGED — split for the file
// cap, P1-E20-W5-S43-T5).
package main

import (
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// recallIndexDir is where the retrieval index lives: {CASCADE_HOME}/data/
// retrieval. It sits under the data directory rather than beside the
// config because, unlike the memory store, it is derived state a user
// never edits by hand: it is rebuilt from the sources, and a corrupt or
// absent one is repaired by rebuilding rather than by opening it.

// registerMemoryAndRecall mounts the memory.* and recall.* namespaces.
// store is threaded through so recall's full-text leg opens over the same
// cascade.db recall.index.rebuild writes into (see registerRecallHandler's
// doc comment).
func registerMemoryAndRecall(
	registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock,
	bus *events.Bus, store provider.Store, memoryAdmin *memory.AdminHandler,
) error {
	registerMemoryHandler(registry, paths, clock, bus, memoryAdmin)
	return registerRecallHandler(registry, paths, bus, store)
}
