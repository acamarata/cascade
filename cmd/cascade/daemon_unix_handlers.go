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
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
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
// registered with no embedder and no vector store, which is the state
// this build is in: no embedding provider is configured at this
// composition root yet, and the full-text leg (F/S-10.T2) has not landed.
// The leg's own contract covers that case — it SKIPS, publishing its
// unavailability event on the real bus rather than inventing vectors — so
// a recall here reports that no retrieval leg is available
// (KindUnavailable) rather than returning an empty result set a user
// would read as an empty index. Adding a leg is a change to this call.
func registerRecallHandler(registry *rpc.Registry, paths runtime.PathProvider, bus *events.Bus) error {
	catalog := recall.NewFileCatalog(filepath.Join(recallIndexDir(paths), recall.CatalogFileName))
	svc, err := recall.NewService(catalog, rrf.Params{}, fusion.NewVectorLeg(nil, nil, bus))
	if err != nil {
		return err
	}
	recall.NewHandler(svc).Register(registry)
	return nil
}
