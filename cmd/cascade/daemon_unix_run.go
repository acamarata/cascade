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
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
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

// buildRPCServer constructs the daemon's real IPC server: POST /rpc
// through an rpc.Registry carrying the MCP dispatcher and the status.get
// handler (D/S-07.T1), and GET /events through an SSEHandler bound to bus.
//
// The SSE handler binds to exactly ONE bus namespace, "daemon". This is
// the disclosed limitation internal/rpc/sse.go's package doc names
// (Bus.Subscribe fans in one namespace, never across namespaces). "daemon"
// is the only namespace anything publishes to at this composition root
// today: internal/daemon/upgrade.go's UpgradeManager publishes
// EventKindShutdownRequested there (its own eventNamespace constant,
// unexported and equal to "daemon"; repeated as a literal here rather
// than imported, since that file is out of scope for this change). A
// future addition of another namespace's producer decides its own SSE
// binding or a real cross-namespace fan-in; that is not invented here.
//
// buildRPCServer also returns the *daemon.Manifest and the active-
// connection *int64 it built the status.get handler against. The caller
// (platformDaemonRun) MUST pass both of these same values on as
// RunOptions.Manifest and RunOptions.Connections, so status.get reads
// Run's real, live subsystem states and connection count rather than a
// second, disconnected copy: this ticket's contract calls this out by
// name ("assemble every field from live daemon state... no mock or
// placeholder return", D/S-07.T1). registerStatusHandler below is the
// composition-root wiring this closes: without it, status.get would exist,
// be tested, and be unreachable from a live daemon, exactly the pattern
// R-14.166 named and forbade going forward.
// rpcServerOption is one further registration buildRPCServer applies to
// the registry it just built. It is variadic rather than a parameter
// because the composition roots that have a policy engine to register are
// not the only callers of buildRPCServer, and a caller with nothing to add
// should not have to name it.
type rpcServerOption func(*rpc.Registry) error

// withPolicyHandlers registers the approval/policy method set built by
// wirePolicy. A nil wiring registers nothing: the verbs are simply absent
// at the far end, which is what a caller that never built a policy engine
// should present, rather than verbs backed by nothing.
func withPolicyHandlers(pol *policyWiring) rpcServerOption {
	return func(registry *rpc.Registry) error {
		if pol == nil || len(pol.Handlers) == 0 {
			return nil
		}
		return daemon.RegisterPolicyHandlers(registry, pol.Handlers)
	}
}

func buildRPCServer(bus *events.Bus, clock runtime.Clock, logger *slog.Logger, settings daemon.Settings, paths runtime.PathProvider, memoryAdmin *memory.AdminHandler, store provider.Store, opts ...rpcServerOption) (*http.Server, *daemon.Manifest, *int64, error) {
	knownEventKind := func(kind events.EventKind) bool {
		return kind == daemon.EventKindShutdownRequested
	}
	sse := rpc.NewSSEHandler(bus, "daemon", knownEventKind, clock)

	registry := rpc.NewRegistry()
	// Register the MCP dispatcher on the daemon's own socket. Without this
	// the transport exists, is tested, and is reachable only through the
	// separate socket the mcp command binds for itself, which is not the
	// socket a client connecting to the daemon uses. The tool registry
	// applies its own exposure filter, so registering the method here does
	// not widen what a caller can reach.
	tools := mcp.NewToolRegistry(plugin.Builtins)
	if err := transport.RegisterSocketMCP(registry, mcp.NewServer(tools)); err != nil {
		return nil, nil, nil, err
	}

	// The memory.* namespace (G/S-13.T3). Registered here for the same
	// reason status.get is: a handler the composition root never mounts is
	// a subsystem that ships built, tested and unreachable.
	registerMemoryHandler(registry, paths, clock, bus, memoryAdmin)

	// The recall.* namespace (F/S-11.T3), registered for the same reason.
	if err := registerRecallHandler(registry, paths, bus); err != nil {
		return nil, nil, nil, err
	}

	// context.scope.show (E/S-08.T4) and context.slice/context.show
	// (E/S-09.T2) — see registerContextEngineHandlers below for why each
	// owns a second sqlite connection instead of threading rawDB in.
	if err := registerContextEngineHandlers(registry, paths, clock); err != nil {
		return nil, nil, nil, err
	}

	// recall.index.* (F/S-11.T4), registered for the same reason. A nil
	// store (some existing test harnesses' minimal buildRPCServer calls)
	// leaves the namespace unregistered rather than reaching into a store
	// that does not exist — see internal/daemon/recall_index.go's doc
	// comment.
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if err := daemon.RegisterRecallIndexHandler(registry, paths, clock, store, dbPath); err != nil {
		return nil, nil, nil, err
	}

	for _, opt := range opts {
		if err := opt(registry); err != nil {
			return nil, nil, nil, err
		}
	}

	// fleet.journal_show/replay (P1-E13-W3-S27-T4), registered for the
	// same reason status.get is: a handler built, tested and never
	// mounted is a subsystem R-14.223 found nothing could reach. The
	// SAME store this composition root already opened is the SAME store
	// wireResumeScan (daemon_resume.go) reads its journal from, so this
	// is a second reader over the one real journal, not a second one.
	daemon.RegisterFleetJournalHandler(registry, store, clock)

	manifest, connections := registerStatusHandler(registry, clock, logger, settings)
	return daemon.NewRPCServer(registry, sse), manifest, connections, nil
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
func registerContextEngineHandlers(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) error {
	if _, err := daemon.RegisterContextScopeHandler(registry, paths, clock); err != nil {
		return err
	}
	if _, err := daemon.RegisterContextAssembleHandler(registry, paths, clock); err != nil {
		return err
	}
	if err := daemon.RegisterContextSyncHandler(registry, paths, clock); err != nil {
		return err
	}
	return nil
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

// recallIndexDir is where the retrieval index lives: {CASCADE_HOME}/data/
// retrieval. It sits under the data directory rather than beside the
// config because, unlike the memory store, it is derived state a user
// never edits by hand: it is rebuilt from the sources, and a corrupt or
// absent one is repaired by rebuilding rather than by opening it.
