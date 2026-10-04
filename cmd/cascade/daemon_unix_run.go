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

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
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

// buildDaemonRegistry does buildRPCServer's composition-root work: it builds
// the POST /rpc *rpc.Registry and the GET /events mux, then runs the
// registrations (wire_<subsystem>.go) over them in phase order: status.get,
// every domain namespace, the options, the fleet handlers, the event topics
// and the MCP dispatcher last. It holds no subsystem name; a new subsystem is
// a new wire_ file. It returns the registry itself, not only an *http.Server,
// so the composition-root tests dispatch in-process without a socket. The
// *daemon.Manifest and connection counter MUST be passed on by composeDaemon
// as RunOptions.Manifest/Connections, so status.get reads Run's real live
// state (D/S-07.T1, R-14.166).
func buildDaemonRegistry(bus *events.Bus, clock runtime.Clock, logger *slog.Logger, settings daemon.Settings, paths runtime.PathProvider, memoryAdmin *memory.AdminHandler, store provider.Store, opts ...rpcServerOption) (*rpc.Registry, *daemon.Manifest, *int64, http.Handler, error) {
	w := &daemonWiring{
		Ctx: context.Background(), Registry: rpc.NewRegistry(), Events: newDaemonEventsMux(bus, clock),
		Bus: bus, Clock: clock, Logger: logger, Settings: settings, Paths: paths, Store: store,
		MemoryAdmin: memoryAdmin, Opts: opts, Deps: daemonRuntimeFromOptions(opts),
	}
	if err := runDaemonWiring(w); err != nil {
		return nil, nil, nil, nil, err
	}
	for _, opt := range opts {
		if opt.observe != nil {
			opt.observe(w.Registry, w.Events)
		}
	}
	return w.Registry, w.Manifest, w.Connections, w.Events, nil
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
