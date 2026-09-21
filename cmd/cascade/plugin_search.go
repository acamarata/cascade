// Purpose: `cascade plugin search [<query>]` (07-CLI-COMMAND-TREE §plugin
//
//	search ✦) and the daemon-side "plugin.search" JSON-RPC handler it
//	dials, built over X/S-50.T1's pkg/plugin.RegistryClient. Also the ONE
//	catalog decision function (pluginCatalogSearch) cmd/cascade/
//	init_adapters.go's wizard step-4 page shares with this command
//	(R-14.93, D2) — see that file for the wizard-side rendering.
//
// FIX-LANE REWRITE (P1-E24-W5-S50-T2, T0 decisions D1-D8,
// /tmp/cascade-evidence/s50t2-t0-decisions.txt), replacing a draft the
// adversarial review returned REWORK on (s50t2-cr-verdict.txt, three
// BLOCKs). What changed, per decision:
//
//	D1: [registry] now comes from internal/runtime.Config (loaded once
//	    via runtime.Load), not raw os.Getenv — see config_registry.go.
//	    No in-binary default public key exists in this tree (that file's
//	    header explains why); an absent or unreadable pubkey_path fails
//	    closed to the builtin catalog with a slog warning AND a doctor
//	    finding (internal/doctor/registry_pubkey.go, wired at
//	    cmd/cascade/doctor_mounts.go).
//	D2: pluginCatalogSearch below is the SAME function
//	    cmd/cascade/init_adapters.go's initPluginCatalog.Entries() calls.
//	D3: cmd/cascade/testdata/scripts/plugin_search.txtar exercises this
//	    command daemonless (no daemon running, no network) — see D6/below
//	    on why that path exists at all.
//	D4: cascade_plugin_search is now a real internal/mcp/coretools Spec
//	    (specs_plugins.go) under a new plugins Capability
//	    (CapabilityPluginsRead); wirePluginSearchHandler is called from
//	    BOTH the daemon's composition root (plugin_rpc.go) and the MCP
//	    tool composition root (mcp_tools.go), so the same "plugin.search"
//	    binding backs the CLI, the daemon RPC surface, and the MCP tool.
//	D5: pluginSearchEntry embeds plugin.RegistryIndexEntry and adds a
//	    Runtime field, so a builtin-fallback row's known runtime survives
//	    to the RUNTIME column (and the wire JSON) instead of always
//	    rendering "-".
//	D6: the RegistryClient is built ONCE per composition root, at wiring
//	    time (buildPluginRegistryClient, plugin_registry_client.go, called
//	    from wirePluginSearchHandler, plugin_rpc.go), never rebuilt per
//	    RPC call.
//	D7/D8: see plugin_search_test.go / plugin_search_catalog_test.go.
//
// FIX-LANE ROUND 2 (T0 decisions D9-D12,
// /tmp/cascade-evidence/s50t2-t0-decisions-2.txt), after the confirming
// review returned FIX-THEN-SHIP:
//
//	D9: pluginSearchCaller branches on daemon CONFIGURED (pluginDaemon
//	    Configured below), never on the probe's dial outcome.
//	D10: registerDBPathHandlers (plugin_rpc.go) gained an UNTAGGED
//	    composition-root test (TestRegisterDBPathHandlersWiresPluginSearch,
//	    plugin_search_catalog_test.go).
//	D11: this file imports NO internal/rpc; wirePluginSearchHandler moved
//	    to plugin_rpc.go, which carries that boundary exemption.
//	D12: init_adapters.go's Entries() mapping is directly tested against
//	    synthetic verified-registry rows (init_cmd_catalog_test.go).
//
// plugin_registry_client.go carries buildPluginRegistryClient/
// resolvePluginRegistryPubkey: split out of this file purely to stay
// under Art.10.3's 300-line cap (config_sections.go's own header records
// the identical prior incident, R-14.204).
//
// SCOPE DEVIATIONS FROM THE TICKET'S OWN files_scope (recorded, per
// LANE-RULES §1): internal/runtime/config_registry.go,
// internal/doctor/registry_pubkey.go(+test), cmd/cascade/doctor_mounts.go,
// internal/mcp/coretools/specs.go+specs_plugins.go, cmd/cascade/mcp_tools.go,
// cmd/cascade/plugin_rpc.go (one call), cmd/cascade/mcp_tools.go (two lines),
// cmd/cascade/init_adapters.go, cmd/cascade/init_cmd.go,
// cmd/cascade/testdata/scripts/plugin_search.txtar
// — every one required by D1-D4 above and none touching a file another
// lane has uncommitted (git status checked before every edit).
//
// SPORT: cli/plugin-search/ADD (P1-E24-W5-S50-T2).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginSearchParams is the "plugin.search" wire request.
type pluginSearchParams struct {
	Q string `json:"q"`
}

// pluginSearchEntry is one catalog row: the registry's own
// RegistryIndexEntry shape (D5) plus a Runtime field the registry's own
// schema does not carry. A builtin-fallback row sets Runtime from
// builtinPluginRow (plugin_builtins.go); a verified registry-sourced row
// leaves it "" — the index format has no such field (pkg/plugin.
// RegistryIndexEntry). Embedding, not a parallel struct: json.Marshal of
// an embedded struct plus one field only ADDS a "runtime" key, so the
// existing RegistryClient.Search() contract and any caller decoding into
// []plugin.RegistryIndexEntry directly are unaffected.
type pluginSearchEntry struct {
	plugin.RegistryIndexEntry
	Runtime string `json:"runtime,omitempty"`
	// fromRegistry is read only by cmd/cascade/init_adapters.go's wizard
	// converter (D2, R-14.93: registry rows render but are never
	// pre-checked). Unexported so it never crosses the JSON wire.
	fromRegistry bool
}

// pluginSearchView is both the wire response and the rendered result.
type pluginSearchView []pluginSearchEntry

// String renders the human table: NAME/VERSION/RUNTIME/DESCRIPTION.
func (v pluginSearchView) String() string {
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "NAME\tVERSION\tRUNTIME\tDESCRIPTION\n")
	for _, e := range v {
		runtimeCol := e.Runtime
		if runtimeCol == "" {
			runtimeCol = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, e.LatestVersion, runtimeCol, truncatePluginDescription(e.Description, 60))
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// truncatePluginDescription cuts s to at most maxRunes runes (rune-safe: a
// byte-index cut could split a multi-byte rune).
func truncatePluginDescription(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// newPluginSearchCmd builds `plugin search`.
func newPluginSearchCmd(deps pluginDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "search [<query>]",
		Short:   "Search the plugin registry catalog (omit <query> to browse)",
		Example: "  cascade plugin search formatter\n  cascade plugin search --json",
		Args:    usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := ""
			if len(args) == 1 {
				q = args[0]
			}
			entries, err := pluginSearchCaller(deps)(cmd.Context(), q)
			if err != nil {
				return err
			}
			if entries == nil {
				entries = []pluginSearchEntry{}
			}
			return pluginOutputWriter(cmd).Result(pluginSearchView(entries))
		},
	}
}

// pluginSearchCaller resolves the function that answers "plugin.search":
// deps.SearchCall when a test injects one; otherwise a daemon round trip
// when a daemon is CONFIGURED for this process (pluginDaemonConfigured,
// D9 — never merely "was a live daemon detected"); and otherwise the SAME
// local catalog decision the daemon's own handler would run, built fresh
// for this one invocation (D3 — cmd/cascade/testdata/scripts/
// plugin_search.txtar exercises this path).
func pluginSearchCaller(deps pluginDeps) func(context.Context, string) ([]pluginSearchEntry, error) {
	if deps.SearchCall != nil {
		return deps.SearchCall
	}
	return func(ctx context.Context, q string) ([]pluginSearchEntry, error) {
		if !pluginDaemonConfigured(ctx, deps.Paths) {
			client, warning := buildPluginRegistryClient(ctx, deps.Paths, deps.Clock)
			if warning != "" {
				slog.Default().Warn(warning)
			}
			return pluginCatalogSearch(ctx, client, q)
		}
		c, err := pluginClient(deps)
		if err != nil {
			return nil, err
		}
		var out pluginSearchView
		if err := c.Do(ctx, "plugin.search", pluginSearchParams{Q: q}, &out); err != nil {
			return nil, err
		}
		return []pluginSearchEntry(out), nil
	}
}

// pluginDaemonConfigured reports whether a daemon is CONFIGURED for this
// process (D9, PRE-FLAG 3 fix) — setup, never a dial's outcome. Either
// signal is sufficient: an explicit [daemon].socket entry in config.toml,
// or a socket file already present at the paths provider's default
// location (a prior `cascade daemon start`, even if nothing answers
// right now). Neither touches the network. A process with neither signal
// (the in-process testscript's fresh CASCADE_HOME) legitimately takes
// D3's local catalog path instead; a config.toml that fails to load
// counts the same as "no [daemon] entry".
func pluginDaemonConfigured(ctx context.Context, paths runtime.PathProvider) bool {
	if cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath()}); err == nil {
		if section, ok := cfg.Extra["daemon"].(map[string]interface{}); ok {
			if sock, ok := section["socket"].(string); ok && sock != "" {
				return true
			}
		}
	}
	_, err := os.Stat(paths.SocketPath())
	return err == nil
}

// pluginSearchRPCHandler builds the "plugin.search" RPC handler over an
// already-built client (or nil — "not configured"/"fails closed"). The
// return type is an unnamed function value, not internal/rpc.HandlerFunc
// by name (D11 — this file must not import internal/rpc); Go converts it
// implicitly wherever a caller assigns it into a rpc.HandlerFunc-typed
// slot (plugin_rpc.go's wirePluginSearchHandler, which carries the
// cmd-rpc-server-boundary exemption this file does not).
func pluginSearchRPCHandler(client *plugin.RegistryClient) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params pluginSearchParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugin.search: decode params")
			}
		}
		entries, err := pluginCatalogSearch(ctx, client, params.Q)
		if err != nil {
			return nil, err
		}
		if entries == nil {
			entries = []pluginSearchEntry{}
		}
		return pluginSearchView(entries), nil
	}
}

// pluginCatalogSearch is the ONE decision behind `plugin search`'s daemon
// RPC handler, its MCP tool, AND cascade init step 4's plugin catalog
// page (R-14.93, D2). The builtin set is always included; a verified,
// reachable registry's matching entries are included ALONGSIDE it — "no
// entries are ever served from an unverified or absent index" (R-14.93)
// means the REGISTRY half fails closed to nothing extra, never that the
// builtin half disappears. client is nil for "not configured" or
// "pubkey absent" (buildPluginRegistryClient's own fail-closed states).
func pluginCatalogSearch(ctx context.Context, client *plugin.RegistryClient, q string) ([]pluginSearchEntry, error) {
	out, err := pluginSearchBuiltinFallback(q)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return out, nil
	}
	registryEntries, err := client.Search(ctx, q)
	if err != nil {
		if cascade.HasKind(err, cascade.KindUnavailable) {
			// Configured and reachable but erroring (ErrRegistryHTTP): a
			// real operational problem, surfaced rather than swallowed.
			return nil, err
		}
		// Any other failure (signature invalid, malformed index —
		// "unverified index") fails closed: the builtin half already
		// collected above, nothing registry-sourced added.
		return out, nil
	}
	for _, e := range registryEntries {
		out = append(out, pluginSearchEntry{RegistryIndexEntry: e, fromRegistry: true})
	}
	return out, nil
}

// pluginSearchBuiltinFallback answers from the compiled-in plugin set,
// case-insensitively matched the same way RegistryClient.Search matches
// (name/description substring) — D5: Runtime carries through from
// builtinPluginRow, which the registry schema itself has no field for.
func pluginSearchBuiltinFallback(q string) ([]pluginSearchEntry, error) {
	rows, err := builtinPluginRows()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(q)
	out := make([]pluginSearchEntry, 0, len(rows))
	for _, r := range rows {
		if needle != "" && !strings.Contains(strings.ToLower(r.Name), needle) {
			continue
		}
		out = append(out, pluginSearchEntry{
			RegistryIndexEntry: plugin.RegistryIndexEntry{ID: r.Name, Name: r.Name, LatestVersion: r.Version},
			Runtime:            r.Runtime,
		})
	}
	return out, nil
}
