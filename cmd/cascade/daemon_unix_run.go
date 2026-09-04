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
	"encoding/json"
	"log/slog"
	"net/http"

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

// registerMemoryHandler mounts the memory.* namespace on the daemon's RPC
// router, rooted at memoryStoreDir (cmd/cascade/memory.go). It lives in
// THIS file rather than beside the command because cmd/cascade may not
// import internal/rpc outside the three composition-root files the
// cmd-rpc-server-boundary rule exempts, and this file is one of them: a
// command that reached for the server package would be one step from
// hand-rolling its own outbound JSON-RPC, which is the thing the boundary
// exists to prevent.
//
// Without this call every memory verb would dial a socket whose far end
// has never heard of memory.*.
func registerMemoryHandler(
	registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus,
	admin *memory.AdminHandler,
) {
	base := memoryStoreDir(paths)
	memory.NewHandler(memory.NewFileStore(base, clock), clock).Register(registry)
	memory.NewSoulHandler(memory.NewFileSoulStore(base, clock, soulDivergenceSink{bus})).Register(registry)
	// admin is nil only where no scheduler was wired (the Windows daemon
	// stub, and any future caller that serves RPC without background
	// jobs). Registering memory.consolidate against a nil handler would
	// turn the verb into a panic at the far end of an RPC, so the verb is
	// simply absent there and the CLI reports an unknown method.
	if admin != nil {
		admin.Register(registry)
	}
}

// soulDivergenceSink publishes the SOUL store's conflict event on the real
// bus. It is the composition root's adapter for the same reason
// runtimeEventBusAdapter above is: internal/memory declares the sink it
// needs structurally and never imports internal/events, so the memory
// store stays testable with no bus at all.
//
// The published payload is the event's own fields — versions, digests, an
// instant — and carries no soul text. That is not incidental: a bus event
// fans out to every subscriber, and the SOUL is the one document in the
// system that must not travel to a subscriber that only asked to know
// something changed.
type soulDivergenceSink struct {
	bus *events.Bus
}

// SoulDiverged publishes ev in the memory namespace. A nil bus discards
// the event, which is the documented no-bus configuration rather than a
// nil-pointer panic inside a store the daemon otherwise serves fine.
func (s soulDivergenceSink) SoulDiverged(ctx context.Context, ev memory.DivergenceEvent) error {
	if s.bus == nil {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.bus.Publish(ctx, soulEventNamespace, events.EventKind(ev.EventName()),
		soulEventSource, payload)
	return err
}

// The bus coordinates the SOUL's divergence event is published under.
const (
	soulEventNamespace = "memory"
	soulEventSource    = "internal/memory"
)

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
func buildRPCServer(bus *events.Bus, clock runtime.Clock, logger *slog.Logger, settings daemon.Settings, paths runtime.PathProvider, memoryAdmin *memory.AdminHandler) (*http.Server, *daemon.Manifest, *int64, error) {
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

	manifest, connections := registerStatusHandler(registry, clock, logger, settings)
	return daemon.NewRPCServer(registry, sse), manifest, connections, nil
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
