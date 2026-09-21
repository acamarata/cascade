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
	if err := registerRecallHandler(registry, paths, bus, store); err != nil {
		return err
	}
	// recall.what (D1, P1-E22-W5-S47-T1): buildRecallWhatHandler
	// (daemon_unix_recall_what.go) does not import internal/rpc, so the
	// actual Register call is here, in this cmd-rpc-server-boundary-exempt
	// file — build-lane-rules item 17/19's documented pattern for a
	// composition root split across a sibling wiring file.
	whatHandler, err := buildRecallWhatHandler(paths, clock, bus, store)
	if err != nil {
		return err
	}
	whatHandler.Register(registry)
	return nil
}
