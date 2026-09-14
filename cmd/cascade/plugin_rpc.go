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

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
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
	registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, store provider.Store, dbPath string,
) error {
	if err := daemon.RegisterRecallIndexHandler(registry, paths, clock, store, dbPath); err != nil {
		return err
	}
	return wirePluginAddHandler(registry, clock, store, dbPath)
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
		rec, err := plugins.ProvisionElevated(
			ctx, db, migrate.SQLiteEmitter{}, clock, "", "", store, domains, m, params.Checksum)
		if err != nil {
			return nil, err
		}
		return pluginAddRPCResult{
			Message: "installed " + rec.Name + " v" + rec.InstalledVersion,
			Grants:  rec.Grants,
		}, nil
	}
}
