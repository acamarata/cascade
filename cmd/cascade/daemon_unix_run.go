//go:build !windows

// Purpose: platformDaemonRun's own composition helpers (opening the real
//
//	Store, adapting it to runtime.EventBus, building the real IPC server,
//	and building the exec-relaunch argv). Split out of daemon_unix.go to
//	stay under the 300-line file cap; this file is the other half of the
//	same composition-root wiring, not a separate concern.
//
// Inputs: daemonDeps (the same injected environment daemon_unix.go's
//
//	other platformDaemon* functions use) plus a *events.Bus and
//	runtime.Clock for buildRPCServer.
//
// Outputs: a real provider.Store (and its closer), a runtime.EventBus
//
//	adapter, a real *http.Server, and the real-executable-prefixed argv
//	RunOptions.Args needs.
//
// Constraints: no bare time.Now/os.Executable outside daemonDeps
//
//	injection. This file is cmd/'s composition root: every dependency
//	still flows in through daemonDeps rather than being read directly.
//
// SPORT: cmd/cascade/daemon (ADD).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// runtimeEventBusAdapter satisfies runtime.EventBus's string-kind Publish
// signature over a real *events.Bus (whose Publish takes a typed
// events.EventKind). runtime.EventBus cannot depend on internal/events
// directly (the import edge runs internal/events may import
// internal/runtime, never the reverse), so this small adapter lives at
// the composition root instead, the same seam MetricsBus already
// documents.
type runtimeEventBusAdapter struct {
	bus *events.Bus
}

func (a *runtimeEventBusAdapter) Publish(ctx context.Context, namespace, kind, source string, payload []byte) error {
	_, err := a.bus.Publish(ctx, namespace, events.EventKind(kind), source, payload)
	return err
}

// openRuntimeStore is now defined in daemon_unix_store.go, alongside the
// real Migrator wiring (migrate.Apply + storage.Bootstrap) it closes
// over. It still opens paths.DataDir()/cascade.db — the SAME paths value
// loadDaemonConfig already resolved from deps.Paths, so this never
// diverges from where the rest of platformDaemonRun looks for CASCADE_HOME
// (see platformDaemonRun's doc comment for why that must stay deps.Paths,
// not a fresh Getenv/HomeDir resolution). The store must exist before the
// recovery scan runs, since Scan's DomainRegistry step needs a real
// registry (built over this same store) as an input, not an output. See
// runtime.StoreDomainRegistry's doc comment for why no such registry
// existed anywhere in the tree before now.

// buildRPCServer constructs the daemon's real IPC server: POST /rpc and
// GET /events, both assembled by buildDaemonRegistry.
func buildRPCServer(bus *events.Bus, clock runtime.Clock, logger *slog.Logger, settings daemon.Settings, paths runtime.PathProvider, memoryAdmin *memory.AdminHandler, store provider.Store, opts ...rpcServerOption) (*http.Server, *daemon.Manifest, *int64, error) {
	registry, manifest, connections, eventsHandler, err := buildDaemonRegistry(bus, clock, logger, settings, paths, memoryAdmin, store, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	return daemon.NewRPCServer(registry, eventsHandler), manifest, connections, nil
}

// buildDaemonRegistry does buildRPCServer's composition-root work: the
// POST /rpc *rpc.Registry (status.get, every domain namespace, then the
// MCP dispatcher) and the GET /events handler (buildFleetSessionsEventsMux).
// It returns the registry itself, not only an *http.Server, so the
// composition-root tests dispatch in-process without a socket (see
// daemon_unix_run_fleet_test.go). The *daemon.Manifest and connection
// counter MUST be passed on by platformDaemonRun as RunOptions.Manifest/
// Connections, so status.get reads Run's real live state (D/S-07.T1,
// R-14.166).
func buildDaemonRegistry(bus *events.Bus, clock runtime.Clock, logger *slog.Logger, settings daemon.Settings, paths runtime.PathProvider, memoryAdmin *memory.AdminHandler, store provider.Store, opts ...rpcServerOption) (*rpc.Registry, *daemon.Manifest, *int64, http.Handler, error) {
	registry := rpc.NewRegistry()
	manifest, connections := registerStatusHandler(registry, clock, logger, settings)
	// The MCP dispatcher is registered on the daemon's own socket (the one
	// clients dial, not the mcp command's separate one), and LAST, so
	// coretools.Registrations sees the finished method table: registered
	// earlier it would expose zero first-party tools (P1-E16-W4-S34-T2).
	// The tool registry applies its own exposure filter.
	registerSocketMCP := func() error {
		return transport.RegisterSocketMCP(registry,
			mcp.NewServer(daemonMCPToolRegistry(registry, mcpFilterFromOptions(opts))))
	}
	if err := registerDaemonNamespaces(registry, manifest, bus, clock, paths, memoryAdmin, store, settings, opts); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := wireFleetSessionsAndCompletion(registry, store, clock, bus, paths, registerSocketMCP); err != nil {
		return nil, nil, nil, nil, err
	}
	return registry, manifest, connections, buildFleetSessionsEventsMux(bus, clock), nil
}

// registerDaemonNamespaces mounts every domain namespace between status.get
// and the fleet/MCP tail, in dependency order. A handler the composition
// root never mounts ships built, tested and unreachable, so each lives
// here; each group is its own helper to keep functions under Art.10.3's
// 50-line cap.
func registerDaemonNamespaces(registry *rpc.Registry, manifest *daemon.Manifest, bus *events.Bus, clock runtime.Clock, paths runtime.PathProvider, memoryAdmin *memory.AdminHandler, store provider.Store, settings daemon.Settings, opts []rpcServerOption) error {
	// memory.* (G/S-13.T3) and recall.* (F/S-11.T3).
	if err := registerMemoryAndRecall(registry, paths, clock, bus, store, memoryAdmin); err != nil {
		return err
	}
	// context.* (E/S-08.T4, E/S-09.T2): see registerContextEngineHandlers.
	if err := registerContextEngineHandlers(registry, paths, clock, bus); err != nil {
		return err
	}
	// conductor.execute and jobs.reachability (R-16.80); after the context
	// handlers because ApplyGraphSchema needs ApplyScopeSchema's foreign-key
	// target in the same cascade.db (daemon_unix_conductor.go).
	if err := wireConductorAndReachability(context.Background(), registry, manifest, paths, clock, store, nodeTunnelLookup(opts)); err != nil {
		return err
	}
	// Supervised background subsystems: the DAG scheduler and
	// cascade-claude's session watch.
	if err := wireSupervisedSubsystems(context.Background(), manifest, bus, clock, paths, store, settings); err != nil {
		return err
	}
	// conductor.expand (R-21.68): see daemon_unix_evidence.go.
	if err := wireConductorExpand(context.Background(), registry, paths, clock); err != nil {
		return err
	}
	// recall.index.* (F/S-11.T4) and plugin.add (D/S-07.T4), each over its
	// own second sqlite connection to dbPath; a nil store leaves both
	// unregistered.
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if err := registerDBPathHandlers(context.Background(), registry, manifest, paths, clock, bus, store, dbPath); err != nil {
		return err
	}
	registry.Register(reviewRPCMethod, reviewRPCHandler) // plugin.review.review, D3; handler in review_mount.go
	if err := applyServerOptions(registry, opts); err != nil {
		return err
	}
	return wireFleetNodeAndJobHandlers(registry, store, clock, bus, paths, settings)
}

// wireFleetSessionsAndCompletion registers fleet.sessions.list (see
// internal/daemon/fleet_sessions_rpc.go), the completion-gate hook pack
// (its call site lives here: see cmd/cascade/hooks.go's header), and last
// the socket MCP dispatcher.
func wireFleetSessionsAndCompletion(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, registerSocketMCP func() error) error {
	daemon.RegisterFleetSessionsHandler(registry, store, clock, bus)
	if err := wireCompletionHookPack(context.Background(), registry, store, clock, bus, paths); err != nil {
		return err
	}
	return registerSocketMCP()
}

// buildFleetSessionsEventsMux builds the GET rpc.EventsPath handler: a
// topic-dispatching *rpc.SSEMux whose default (no-topic) leg is the
// daemon-wide SSEHandler and whose one topic is fleet.sessions. The
// default leg binds exactly ONE bus namespace, "daemon" (the limitation
// internal/rpc/sse.go's package doc names): the only namespace this
// composition root publishes daemon-wide events to (upgrade.go's
// EventKindShutdownRequested, plus the job-lease, supervisor and
// status-widget kinds). FAIL-CLOSED: any other topic shape is refused by
// the mux, never served the default stream (internal/rpc/sse_mux.go).
func buildFleetSessionsEventsMux(bus *events.Bus, clock runtime.Clock) *rpc.SSEMux {
	knownEventKind := rpc.CombineKnownEventKind(func(kind events.EventKind) bool {
		return kind == daemon.EventKindShutdownRequested
	}, rpc.KnownJobLeaseEventKind, rpc.KnownSupervisorEventKind, daemon.KnownStatusWidgetEventKind)
	sse := rpc.NewSSEHandler(bus, "daemon", knownEventKind, clock)
	return rpc.NewSSEMux(sse).WithTopic(sessions.Topic, sessions.NewSSEHandler(bus, clock))
}

// mcpFilterFromOptions builds the capability filter the daemon's MCP tool
// registry consults, over the SAME policy engine every other gated call
// site in this process shares.
//
// No engine in the options means no filter: a daemon that could not build
// its policy engine exposes no capability-gated tool, which is the same
// fail-closed answer mcpToolFilter gives the stdio path for the same
// reason.
func mcpFilterFromOptions(opts []rpcServerOption) mcp.CapabilityFilter {
	for _, opt := range opts {
		if opt.policyEngine != nil {
			return coretools.NewPolicyFilter(opt.policyEngine, mcpToolSubject)
		}
	}
	return mcp.DenyAllFilter{}
}

// registerContextEngineHandlers registers context.scope.show (E/S-08.T4),
// context.slice/context.show (E/S-09.T2) and context.sync (E/S-09.T4)
// together, factored out of buildRPCServer purely to stay under
// Art.10.3's 50-line function cap (mechanical relocation, same
// composition-root concern buildRPCServer's own doc comment already
// names). The first two registrations open their own second sqlite
// connection to paths.DataDir()/cascade.db rather than reusing
// platformDaemonRun's rawDB — see internal/daemon/context_scope.go's and
// internal/daemon/context_assemble.go's doc comments for why: threading
// rawDB into this function's signature would ripple into call sites
// outside either ticket's files_scope, and a second connection is a
// documented no-op after the first opens it (every schema apply here is
// idempotent by contract). context.sync opens no connection at all — see
// internal/daemon/context_sync.go's doc comment.
func registerContextEngineHandlers(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus) error {
	if _, err := daemon.RegisterContextScopeHandler(registry, paths, clock); err != nil {
		return err
	}
	if _, err := daemon.RegisterContextAssembleHandler(registry, paths, clock); err != nil {
		return err
	}
	if err := daemon.RegisterContextSyncHandler(registry, paths, clock); err != nil {
		return err
	}
	// context.hydration.degraded (P1-E16-W4-S34-T4): the prompt-hydration
	// hook's telemetry door. It exists because the store takes an
	// EXCLUSIVE lock — a hook that published directly while this daemon
	// held the database would fail in exactly the common case.
	daemon.RegisterContextHydrationHandler(registry, bus, clock)
	// context.harness_list / context.harness_sync (P1-E16-W4-S35-T3):
	// 07's harness surface, served beside the context namespace it folds
	// under.
	return daemon.RegisterContextHarnessHandlers(registry, paths, clock)
}

// registerStatusHandler builds this composition root's *daemon.Manifest
// and active-connection counter, registers status.get against them, and
// returns both so the caller can hand the SAME values to daemon.Run via
// RunOptions: the one seam that makes status.get's Connections and
// Health fields real instead of stubbed (see buildRPCServer's doc
// comment). The start time is read here, immediately before Run's own
// socket/pidfile setup, so uptime is accurate to within the few
// milliseconds of composition-root work between the two, using the SAME
// injected clock Run itself uses (Art.7.1).
func relaunchExecArgs(deps daemonDeps) func() []string {
	return func() []string {
		execPath, err := deps.Executable()
		if err != nil {
			return nil
		}
		return append([]string{execPath}, relaunchArgs()...)
	}
}

// runRecoveryScan runs the real crash-recovery scan (runtime.Scan) over
// store's DomainRegistry — split out of platformDaemonRun under
// Art.10.3's 50-line function cap (mechanical relocation, same
// composition-root concern, not a new one).
func runRecoveryScan(ctx context.Context, paths runtime.PathProvider, settings daemon.Settings, deps daemonDeps, logProvider *runtime.LogProvider, bus *events.Bus, store provider.Store) error {
	_, err := runtime.Scan(ctx, runtime.RecoveryOptions{
		PidfilePath: daemon.PIDFilePath(paths),
		SocketPath:  settings.SocketPath,
		Clock:       deps.Clock,
		Log:         logProvider.Logger(),
		Bus:         &runtimeEventBusAdapter{bus: bus},
		Registry:    runtime.NewStoreDomainRegistry(store),
	})
	return err
}
