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
	"context"
	"path/filepath"
	"sync"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/embeddings"
	"github.com/acamarata/cascade/providers/localvector"

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

// registerStatusHandler registers status.get over manifest, the daemon's
// one Manifest (composeDaemon creates it before any supervised goroutine
// starts), and returns the active-connection counter RunOptions shares.
func registerStatusHandler(registry *rpc.Registry, clock runtime.Clock, manifest *daemon.Manifest, settings daemon.Settings) *int64 {
	connections := new(int64)
	provider := daemon.NewStatusProvider(clock, clock.Now(), settings.SocketPath, connections, manifest)
	registry.Register(daemon.StatusMethod, provider.Handler())
	return connections
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
	legs := []recall.Leg{recallVectorLeg(processEmbedder(paths), store, bus)}
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

// laneInterceptor runs every lane input through the process firewall's
// engine under the conductor egress class, at the stamped tier or the
// embedder's own, whichever is more restrictive. The engine is built on
// first use; a failure is not cached.
type laneInterceptor struct {
	engine func(*egress.Registry) (*egress.Engine, error)
	mu     sync.Mutex
	built  *egress.Engine
}

// InterceptClass checks content for the lane.
func (l *laneInterceptor) InterceptClass(ctx context.Context, _ embeddings.EgressClass, own provider.SensitivityTier, content []byte) ([]byte, error) {
	tier, err := stampedLaneTier(ctx)
	if err != nil {
		return nil, err
	}
	if own.MoreRestrictiveThan(tier) {
		return nil, cascade.Newf(cascade.KindPolicyDenied, "embedding: refused at tier %s", own)
	}
	engine, err := l.engineOnce()
	if err != nil {
		return nil, err
	}
	return engine.InterceptClass(ctx, conductor.EgressClassConductor, tier, content)
}

// engineOnce returns the engine, building it on a registry holding the
// conductor egress class.
func (l *laneInterceptor) engineOnce() (*egress.Engine, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.built != nil {
		return l.built, nil
	}
	if l.engine == nil {
		return nil, egress.ErrNoProcessFirewall
	}
	reg := egress.NewRegistry()
	if err := conductor.RegisterEgressClassConductor(reg); err != nil {
		return nil, err
	}
	e, err := l.engine(reg)
	if err != nil {
		return nil, err
	}
	l.built = e
	return e, nil
}

// firewallEngineFor is the production engine factory: the process
// firewall's Engine over the registry (P1-SEC-05).
func firewallEngineFor(paths runtime.PathProvider) func(*egress.Registry) (*egress.Engine, error) {
	return func(r *egress.Registry) (*egress.Engine, error) {
		fw, err := processFirewallFor(paths)
		if err != nil {
			return nil, err
		}
		return fw.Engine(r)
	}
}

// recallVectorLeg is the query-time vector leg over sel: no embedder when
// sel is unusable or there is no store (the leg then reports itself
// unavailable), and a personal scope is admitted only for a local embedder.
func recallVectorLeg(sel *embedsel.Selection, store provider.Store, bus *events.Bus) *fusion.VectorLeg {
	if sel == nil || sel.Err() != nil || store == nil {
		return fusion.NewVectorLeg(nil, nil, bus)
	}
	return fusion.NewVectorLegWith(sel.FusionEmbedder(), localvector.New(store), bus,
		fusion.VectorLegOptions{AdmitPersonal: sel.Admits(provider.SensitivityLocalOnly)})
}

// recallEmbeddingFor is recall.index.*'s embedding half over sel: none when
// sel is unusable or there is no store, else sel's embedder over the same
// vector store the query leg reads, admitting only the corpora sel admits.
func recallEmbeddingFor(sel *embedsel.Selection, store provider.Store) []daemon.RecallEmbedding {
	if sel == nil || sel.Err() != nil || store == nil {
		return nil
	}
	return []daemon.RecallEmbedding{{Embedder: sel.Embedder(), Vectors: localvector.New(store), AdmitCorpus: sel.AdmitsCorpus}}
}
