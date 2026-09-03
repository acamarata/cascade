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
	"net/http"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
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

// openRuntimeStore opens the real on-disk cascade.db (providers/sqlite's
// modernc-sqlite driver) under paths.DataDir() — the SAME paths value
// loadDaemonConfig already resolved from deps.Paths, so this never
// diverges from where the rest of platformDaemonRun looks for CASCADE_HOME
// (see platformDaemonRun's doc comment for why that must stay deps.Paths,
// not a fresh Getenv/HomeDir resolution). The store must exist before the
// recovery scan runs, since Scan's DomainRegistry step needs a real
// registry (built over this same store) as an input, not an output. See
// runtime.StoreDomainRegistry's doc comment for why no such registry
// existed anywhere in the tree before now.
func openRuntimeStore(ctx context.Context, paths runtime.PathProvider) (provider.Store, func(), error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "create data directory")
	}
	driver, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		return nil, nil, err
	}
	return driver, func() { _ = driver.Close() }, nil
}

// buildRPCServer constructs the daemon's real IPC server: POST /rpc
// through an empty rpc.Registry (no RPC method has been registered onto
// it yet anywhere in this repo; registering the real method set is a
// separate concern from composition-root wiring) and GET /events through
// an SSEHandler bound to bus.
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
func buildRPCServer(bus *events.Bus, clock runtime.Clock) *http.Server {
	knownEventKind := func(kind events.EventKind) bool {
		return kind == daemon.EventKindShutdownRequested
	}
	sse := rpc.NewSSEHandler(bus, "daemon", knownEventKind, clock)
	return daemon.NewRPCServer(rpc.NewRegistry(), sse)
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
