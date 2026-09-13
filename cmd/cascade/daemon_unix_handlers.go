// Purpose: the status and recall handler registrations, split out of
//
//	daemon_unix_run.go so that file stays under the size cap. Every lane
//	that registers something edits that one file, so it sits permanently
//	at the ceiling; keeping the registrations in siblings gives the next
//	ticket room without a merge conflict over a shared line.
//
// SPORT: cmd/cascade/daemon (CHANGED, registration split).
package main

import (
	"log/slog"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

func registerStatusHandler(registry *rpc.Registry, clock runtime.Clock, logger *slog.Logger, settings daemon.Settings) (*daemon.Manifest, *int64) {
	manifest := daemon.NewManifest(logger, clock)
	connections := new(int64)
	provider := daemon.NewStatusProvider(clock, clock.Now(), settings.SocketPath, connections, manifest)
	registry.Register(daemon.StatusMethod, provider.Handler())
	return manifest, connections
}

// relaunchExecArgs builds RunOptions.Args: the argv UpgradeManager's
// exec-relaunch (syscall.Exec) re-invokes the binary with. It mirrors
// relaunchArgs (daemon.go's background-start argv) prefixed with the
// resolved executable path as argv[0], the shape syscall.Exec expects.
// RunOptions.Args's default, when unset, is just the bare executable path
// with no subcommand, which resumes nothing meaningful; supplying this
// closes that gap for the in-place-relaunch path specifically.

func recallIndexDir(paths runtime.PathProvider) string {
	return filepath.Join(paths.DataDir(), "retrieval")
}

// registerRecallHandler mounts the recall.* namespace on the daemon's RPC
// router, reading the index catalog under recallIndexDir. It lives in
// THIS file for the reason registerMemoryHandler does: cmd/cascade may
// not import internal/rpc outside the composition-root files the
// cmd-rpc-server-boundary rule exempts, and this is one of them.
//
// Without this call every `cascade recall` would dial a socket whose far
// end has never heard of recall.query.
//
// WHICH LEGS ARE WIRED, stated plainly. The query-time vector leg is
// registered with no embedder and no vector store: no embedding provider
// is configured at this composition root yet, and the leg's own contract
// covers that case by SKIPPING rather than inventing vectors. The
// full-text leg (F/S-10.T2, internal/retrieval.NewLeg) is wired over the
// SAME store this call's own caller (buildRPCServer) already opened for
// every other domain's RPC namespace — the identical store
// RegisterRecallIndexHandler builds recall.index.rebuild's own index
// over (internal/daemon/recall_index.go's mustIndex) — so a rebuilt index
// and a live query read the same rows. A nil store (some existing test
// harnesses' minimal buildRPCServer calls) leaves the full-text leg
// unregistered rather than reaching into a store that does not exist,
// the same degradation RegisterRecallIndexHandler already applies. Only
// once neither leg is configured does a recall here report that no
// retrieval leg is available (KindUnavailable).
func registerRecallHandler(registry *rpc.Registry, paths runtime.PathProvider, bus *events.Bus, store provider.Store) error {
	catalog := recall.NewFileCatalog(filepath.Join(recallIndexDir(paths), recall.CatalogFileName))
	legs := []recall.Leg{fusion.NewVectorLeg(nil, nil, bus)}
	if store != nil {
		idx, err := retrieval.NewIndex(store)
		if err != nil {
			return err
		}
		legs = append(legs, retrieval.NewLeg(idx))
	}
	svc, err := recall.NewService(catalog, rrf.Params{}, legs...)
	if err != nil {
		return err
	}
	recall.NewHandler(svc).Register(registry)
	return nil
}
