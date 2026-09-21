// Purpose: registers the "plugin.add" JSON-RPC method (D/S-07.T4) on the
// daemon's router, over the real internal/plugins.ProvisionElevated
// composition root — closing the R-16.80 gate TestRPCMethodGate_
// RealTreeGreen found: this package's own plugin_add.go dials "plugin.add"
// and, before this ticket's COMPLETION PASS, nothing in the tracked tree
// registered it.
//
// CONTRACT NOTE (files_scope, same shape as hooks.go's own CONTRACT NOTE
// it cites): this file, and the one-line call it needs from
// buildRPCServer (daemon_unix_run.go), earn a cmd-rpc-server-boundary
// exemption (.golangci.yml + internal/client/boundary_test.go's
// cmdRPCBoundaryExempt map, edited together) on the SAME daemon-SIDE
// grounds as hooks.go: it REGISTERS a method on the daemon's own
// registry and never dials the daemon. It cannot live in internal/daemon
// like RegisterRecallIndexHandler does: internal/plugins (via
// cascadepa_wiring.go/cascadepa_review_wiring.go/cascadepa_soul_wiring.go,
// which import internal/client) is downstream of internal/client, which
// itself imports internal/daemon (context_scope.go, status.go) — so
// internal/daemon importing internal/plugins is a real import cycle
// (proven by `go build ./...`: "import cycle not allowed" through
// exactly that chain). cmd/cascade has no such constraint: it already
// imports both internal/plugins (plugin.go) and internal/daemon
// (daemon_unix_run.go) without incident, since nothing imports cmd/cascade
// back.
//
// Inputs: the real *rpc.Registry, runtime.Clock, the already-open
// provider.Store (cmd/cascade/daemon_unix_store.go's openRuntimeStore)
// every sibling registerXHandler/wireX call shares, and dbPath —
// buildRPCServer already computes this for RegisterRecallIndexHandler.
//
// Outputs: "plugin.add" bound to a real handler that parses the elevated
// manifest the CLI sends, provisions the plugin's isolated storage
// domain, and commits its metadata record — see
// internal/plugins/dispatch.go's ProvisionElevated for what "provisions"
// means and why a process-tier manifest always refuses today.
//
// Constraints: opens its own second sqlite connection to dbPath, left
// open for the daemon process's lifetime — the same disclosed tradeoff
// hooks.go's wireCompletionHookPack and internal/daemon/recall_index.go's
// RegisterRecallIndexHandler both already carry (threading a shared
// *sql.DB into buildRPCServer's signature would touch every existing call
// site and test, none of which are this ticket's). A nil store leaves the
// namespace unregistered, mirroring registerMemoryHandler's own nil-store
// degradation.
//
// SPORT: cmd/cascade plugin.add handler (ADD) — P1-E15-W4-S32-T4
// COMPLETION PASS.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// registerDBPathHandlers registers recall.index.* (F/S-11.T4) and
// plugin.add (D/S-07.T4), both of which open their own second sqlite
// connection to dbPath rather than reusing platformDaemonRun's rawDB (the
// same tradeoff registerContextEngineHandlers, daemon_unix_run.go,
// documents for its own two namespaces). Factored out of buildRPCServer
// and relocated here (rather than left inline in daemon_unix_run.go)
// purely to keep that file under Art.10.3's 300-line file cap.
func registerDBPathHandlers(
	ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest,
	paths runtime.PathProvider, clock runtime.Clock,
	bus *events.Bus, store provider.Store, dbPath string,
) error {
	if err := daemon.RegisterRecallIndexHandler(registry, paths, clock, store, dbPath); err != nil {
		return err
	}
	// chat.* (T/S-43.T2) belongs in this group by the same description:
	// a namespace over its own second connection to this same cascade.db.
	// It registers even when store is nil, because it does not use the
	// shared store at all — and because a chat surface that silently did
	// not register is precisely the defect this wiring closed (R-14.284).
	// The chat scrub pipeline's vault custody is selected HERE, in the
	// composition root, and handed to wireChatHandlers — see that
	// function's doc comment for why it must not select one itself. The
	// service label is vault.go's, so a scrubbed turn's secret lands in
	// the same vault `cascade vault` reads.
	custody, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: paths.DataDir()})
	if err != nil {
		return err
	}
	if err := wireChatHandlers(ctx, registry, paths, clock, bus, custody); err != nil {
		return err
	}
	// The cascade-pa TELEGRAM BRIDGE — the other half of this plugin's daemon
	// surface, and the one that needs a process with a lifetime: see
	// wireCascadePABridge below.
	wireCascadePABridge(ctx, registry, manifest, paths, clock, bus)
	// plugin.search (X/S-50.T2, SCOPE DEVIATION: this call is the only
	// line this ticket adds outside its own declared files_scope, added
	// here per the ticket's own §5 fallback -- "put it where the existing
	// plugin.* handlers live" -- because that is where every other
	// plugin.* method is wired onto the daemon's real composition root.
	// wirePluginSearchHandler itself, and everything it calls, lives in
	// plugin_search.go (in files_scope.add). It needs no store/db, so it
	// registers unconditionally rather than behind the `if store == nil`
	// guard wirePluginAddHandler uses just above.
	wirePluginSearchHandler(ctx, registry, paths, clock)
	return wirePluginAddHandler(registry, clock, store, dbPath)
}

// wirePluginSearchHandler registers "plugin.search" on registry, over a
// RegistryClient built ONCE, here, at wiring time (D6) — never rebuilt
// per call. Called from registerDBPathHandlers above (the daemon) and
// mcp_tools.go's registerMCPToolMethods (the MCP tool process). Lives
// here, not in plugin_search.go (D11): this file carries the
// cmd-rpc-server-boundary exemption (registers, never dials);
// plugin_search.go does not and must not import internal/rpc —
// pluginSearchRPCHandler there returns an unnamed func value, converted
// implicitly on assignment to Register below.
func wirePluginSearchHandler(ctx context.Context, registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) {
	client, warning := buildPluginRegistryClient(ctx, paths, clock)
	if warning != "" {
		slog.Default().Warn(warning)
	}
	registry.Register("plugin.search", pluginSearchRPCHandler(client))
}

// wireCascadePABridge mounts Epic W's Telegram bridge on this daemon: the poll
// goroutine as a tracked subsystem, its drain on shutdown, and pa.pair_code so
// `cascade pa pair` issues codes through the process that verifies them.
//
// It never fails the daemon's startup. A bridge that cannot be assembled at all
// (an unreadable data directory, a malformed module manifest) is recorded as a
// FAILED subsystem and the daemon continues; a bridge that is merely not
// enabled — the shipped state — is recorded as disabled. Refusing to start the
// daemon because an opt-in chat bridge is misconfigured would trade a missing
// convenience for a dead host.
//
// The custody selection is made HERE, in the composition root, and handed in:
// a bridge that selected its own could not be exercised without reaching the
// operator's real keychain (R-14.206), the same reason wireChatHandlers takes
// its custody as a parameter.
//
// WHY THE POLL CONTEXT IS SIGNAL-DERIVED (disclosed, with the follow-up named).
// buildRPCServer is handed no run context — every namespace above it is
// registered under context.Background() — and threading Run's context through
// its signature would touch every existing call site and test, which is the
// same disclosed tradeoff this file's header already carries for dbPath. The
// bridge needs a context that really ENDS, or its drain would never run, so it
// derives one from the signals daemon.Run itself shuts down on (SIGTERM is
// exactly what `cascade daemon stop` sends, lifecycle_unix_stop.go). Go fans a
// signal out to every registered subscriber, so this does not take the signal
// away from Run's own handler. Cancelling the ctx passed in works too, which is
// what the internal/daemon tests drive; threading the real run context remains
// the cleaner end state.
func wireCascadePABridge(ctx context.Context, registry *rpc.Registry, manifest *daemon.Manifest,
	paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus) {
	if manifest == nil {
		return
	}
	rt, err := plugins.NewCascadePABridge(ctx, plugins.BridgeDeps{
		DataDir: paths.DataDir(),
		Vault:   plugins.BridgeVaultConfig(paths.DataDir()),
		Clock:   clock,
		Events:  bus,
	})
	if err != nil {
		manifest.RegisterBridgeModule(ctx, registry, daemon.BridgeSubsystem{DisabledReason: err.Error()})
		return
	}
	if rt.Start == nil {
		// Not enabled: no poll to stop, so no signal subscription is taken out
		// at all. pa.pair_code is still registered, and answers with the
		// runtime's own typed refusal naming what is missing.
		manifest.RegisterBridgeModule(ctx, registry, daemon.BridgeSubsystem{
			IssueCode: bridgePairCodeIssuer(rt), DisabledReason: rt.DisabledReason,
		})
		return
	}
	pollCtx, releaseSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	manifest.RegisterBridgeModule(pollCtx, registry, daemon.BridgeSubsystem{
		Start: rt.Start,
		// releaseSignals runs after the drain, so the subscription lives
		// exactly as long as the poll it exists to stop.
		Stop:      func(c context.Context) error { defer releaseSignals(); return rt.Stop(c) },
		IssueCode: bridgePairCodeIssuer(rt),
	})
}

// bridgePairCodeIssuer adapts the plugin runtime's issuance closure onto the
// daemon's wire result. The RFC3339 rendering happens here, at the boundary,
// so neither side carries the other's formatting choice.
func bridgePairCodeIssuer(rt *plugins.BridgeRuntime) func(context.Context, string) (daemon.BridgePairCodeResult, error) {
	return func(ctx context.Context, subject string) (daemon.BridgePairCodeResult, error) {
		res, err := rt.IssueCode(ctx, subject)
		if err != nil {
			return daemon.BridgePairCodeResult{}, err
		}
		return daemon.BridgePairCodeResult{
			Code:      res.Code,
			Subject:   res.Subject,
			ExpiresAt: res.ExpiresAt.UTC().Format(time.RFC3339),
		}, nil
	}
}

// pluginAddRPCResult mirrors plugin_add.go's pluginAddView field-for-
// field. The request side reuses plugin_add.go's own pluginAddParams
// directly (same package); the response side keeps its own small type
// instead of reusing pluginAddView so this handler's wire shape does not
// silently drift if that CLI-side view type ever grows a CLI-only field.
type pluginAddRPCResult struct {
	Message string   `json:"message"`
	Grants  []string `json:"grants,omitempty"`
}

// wirePluginAddHandler mounts "plugin.add" on registry, over a
// *storage.PluginDomainRegistry built ONCE here and captured by the
// returned handler closure for the daemon process's lifetime (the
// registry has no persistence of its own — internal/storage.
// NewPluginDomainRegistry's doc comment: "returns an empty registry";
// re-building it per call would make every domain claim look new). A nil
// store leaves the namespace unregistered rather than reaching into a
// store that does not exist.
func wirePluginAddHandler(registry *rpc.Registry, clock runtime.Clock, store provider.Store, dbPath string) error {
	if store == nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "plugin.add: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	// db is intentionally not closed here: it lives for the daemon
	// process's lifetime, the same disclosed gap wireCompletionHookPack's
	// own call site (hooks.go) already carries.

	domains := storage.NewPluginDomainRegistry()
	registry.Register("plugin.add", pluginAddRPCHandler(db, clock, store, domains))
	return nil
}

// pluginAddRPCHandler builds the "plugin.add" rpc.HandlerFunc. The
// migrator's own dbPath/backupDir snapshot feature (§D-18) is left
// disabled (both ""): a plugin.add's own migration call always passes a
// nil migration list (dispatch.go's provisionStorageDomain), so no DDL
// ever runs through this migrator today, and there is nothing yet to
// snapshot.
func pluginAddRPCHandler(
	db *sql.DB, clock runtime.Clock, store provider.Store, domains *storage.PluginDomainRegistry,
) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params pluginAddParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugin.add: decode params")
		}
		m, err := plugin.ParseManifest(bytes.NewReader(params.ManifestBytes))
		if err != nil {
			return nil, err
		}
		if m.ID != params.ID {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"plugin.add: manifest id %q does not match requested id %q", m.ID, params.ID)
		}
		// enableRemoteRuntime is hardcoded false here: wirePluginAddHandler
		// (below) is not handed a *runtime.Config today, and threading one
		// through buildRPCServer's signature to reach it would touch every
		// existing call site and test, none of which are this ticket's —
		// the same disclosed tradeoff this file's own header comment
		// already states for dbPath/backupDir. A remote-runtime `plugin
		// add` over this RPC path therefore always takes the Art.1.3
		// deferred-warning branch (remote.ErrRemoteRuntimeNotEnabled)
		// until a later ticket threads the live [plugins].enable_remote_runtime
		// value in; ProvisionElevated's RuntimeRemote branch builds its
		// own real egress.Engine-backed Interceptor when that flag does
		// flip true and no caller-supplied one is given (dispatch_remote.go).
		rec, err := plugins.ProvisionElevated(
			ctx, db, migrate.SQLiteEmitter{}, clock, "", "", store, domains, m, params.Checksum, false, nil)
		if err != nil {
			return nil, err
		}
		return pluginAddRPCResult{
			Message: "installed " + rec.Name + " v" + rec.InstalledVersion,
			Grants:  rec.Grants,
		}, nil
	}
}
