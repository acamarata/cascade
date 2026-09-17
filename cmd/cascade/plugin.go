// Purpose: `cascade plugin` (07-CLI-COMMAND-TREE §cascade plugin, minus
// `search`, which belongs to X/S-50) — the cobra mount plus the two
// read-only verbs, list and info. add/enable/disable/remove/perms/update
// each get their own sibling file under this package's established
// 300-line-cap split convention (approval.go/approval_standing.go).
//
// Inputs: cobra flags plus an injected pluginDeps, so no test touches a
// real socket, a real filesystem outside t.TempDir, or the real
// environment (Art.7.1).
//
// Outputs: process output through internal/output.Writer — the versioned
// JSON envelope under --json, a human table otherwise.
//
// Constraints: list/info/enable/disable/remove need no elevation (§5.14
// names none of them) and have a real embedded (daemonless) path —
// plugin_store_unix.go/plugin_store_windows.go open the same cascade.db
// the daemon itself uses, so an operator never needs a running daemon just
// to see or toggle what is installed. add/perms/update are different: they
// need the daemon per D/S-07.T4 (see plugin_add.go's doc comment for why).
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// pluginDialTimeout bounds a daemon round trip for the elevated verbs,
// matching every other command group's established value.
const pluginDialTimeout = 5 * time.Second

// pluginDeps carries every external input the plugin command group needs.
type pluginDeps struct {
	Paths       runtime.PathProvider
	Clock       runtime.Clock
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
	Getenv      runtime.Getenv
	Stdin       func() []byte // reads one confirmation line; nil in production, real in tests
}

// productionPluginDeps is the real environment.
func productionPluginDeps() pluginDeps {
	return pluginDeps{Paths: lazyPaths{}, Clock: runtime.NewSystemClock(), DialContext: client.UnixDialer, Getenv: os.Getenv}
}

// mountPluginCmd attaches the top-level `plugin` command group. Sole mount
// point, called from root.go's mountSubcommands.
func mountPluginCmd(root *cobra.Command) {
	cmd := newPluginCmd(productionPluginDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newPluginCmd builds the whole `plugin` tree.
func newPluginCmd(deps pluginDeps) *cobra.Command {
	pluginCmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage the plugin lifecycle: install, inspect, and remove",
		Long: "Manage installed plugins. list/info/enable/disable/remove work without a\n" +
			"running daemon; add, perms grant/revoke, and update are elevated (or can\n" +
			"become elevated) and always need one (D/S-07.T4).",
	}
	pluginCmd.AddCommand(newPluginListCmd(deps))
	pluginCmd.AddCommand(newPluginInfoCmd(deps))
	pluginCmd.AddCommand(newPluginEnableCmd(deps))
	pluginCmd.AddCommand(newPluginDisableCmd(deps))
	pluginCmd.AddCommand(newPluginRemoveCmd(deps))
	pluginCmd.AddCommand(newPluginAddCmd(deps))
	pluginCmd.AddCommand(newPluginPermsCmd(deps))
	pluginCmd.AddCommand(newPluginUpdateCmd(deps))
	return pluginCmd
}

// pluginOutputWriter mirrors every other command group's per-file convention.
func pluginOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// pluginDaemonAvailable reports whether this invocation has a live daemon
// connection, per the same probe every other daemonless command consults.
// An undecidable probe result (ok=false) is treated as unavailable — the
// conservative direction for an elevation gate (see LANE-RULES §4 and
// grantsExpand's own doc comment on "fail closed when unsure").
func pluginDaemonAvailable(ctx context.Context) bool {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	return ok && !st.Embedded
}

// pluginClient resolves the daemon socket and returns a client for it. Only
// the elevated verbs call this.
func pluginClient(deps pluginDeps) (*client.Client, error) {
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), pluginDialTimeout), nil
}

// pluginNoInput reports whether CASCADE_NO_INPUT=1 is set.
func pluginNoInput(deps pluginDeps) bool {
	if deps.Getenv == nil {
		return false
	}
	return deps.Getenv("CASCADE_NO_INPUT") == "1"
}

// newPluginListCmd builds `plugin list`.
func newPluginListCmd(deps pluginDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List installed plugins",
		Example: "  cascade plugin list --json",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			recs, err := withPluginStore(cmd.Context(), deps, func(ctx context.Context, store provider.Store) ([]plugins.PluginMetadata, error) {
				return plugins.ListMetadata(ctx, store)
			})
			if err != nil {
				return err
			}
			// The builtins are listed alongside, because they are what
			// this binary actually ships and an operator who just ran
			// `cascade init` was told about them by name.
			builtins, loadErr := builtinPluginRows()
			if loadErr != nil {
				return loadErr
			}
			return pluginOutputWriter(cmd).Result(pluginListView{Installed: recs, Builtin: builtins})
		},
	}
}

// pluginListView is what `plugin list` renders: everything this host has,
// from both populations.
type pluginListView struct {
	Installed []plugins.PluginMetadata `json:"installed"`
	Builtin   []builtinPluginRow       `json:"builtin"`
}

// String lists both, with a SOURCE column, because "installed" and
// "compiled in" behave differently: one can be removed and disabled, the
// other cannot.
func (v pluginListView) String() string {
	if len(v.Installed) == 0 && len(v.Builtin) == 0 {
		return "this build ships no plugins and none are installed"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "NAME\tSOURCE\tVERSION\tENABLED\tRUNTIME\n")
	for _, r := range v.Builtin {
		// Always enabled: nothing reads an enabled flag for a builtin,
		// and printing "false" for one whose commands work would be the
		// same contradiction in a narrower place.
		_, _ = fmt.Fprintf(tw, "%s\tbuiltin\t%s\ttrue\t%s\n", r.Name, r.Version, r.Runtime)
	}
	for _, r := range v.Installed {
		_, _ = fmt.Fprintf(tw, "%s\tinstalled\t%s\t%t\t%s\n",
			r.Name, r.InstalledVersion, r.Enabled, r.RuntimeMode)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// newPluginInfoCmd builds `plugin info <name>`.
func newPluginInfoCmd(deps pluginDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "info <name>",
		Short: "Show one installed plugin's full record",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A builtin answers from the registry: it has no store
			// record, and saying it is not installed was the same
			// contradiction `plugin list` used to produce.
			if builtin, ok := builtinPluginByName(args[0]); ok {
				return pluginOutputWriter(cmd).Result(builtin)
			}
			rec, err := withPluginStore(cmd.Context(), deps, func(ctx context.Context, store provider.Store) (plugins.PluginMetadata, error) {
				rec, ok, err := plugins.LoadMetadata(ctx, store, args[0])
				if err != nil {
					return plugins.PluginMetadata{}, err
				}
				if !ok {
					return plugins.PluginMetadata{}, cascade.Newf(cascade.KindNotFound,
						"plugin: %q is neither installed nor built into this binary; "+
							"`cascade plugin list` shows both", args[0])
				}
				return rec, nil
			})
			if err != nil {
				return err
			}
			return pluginOutputWriter(cmd).Result(pluginInfoView(rec))
		},
	}
}

type pluginInfoView plugins.PluginMetadata

func (v pluginInfoView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "FIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "name\t%s\n", v.Name)
	_, _ = fmt.Fprintf(tw, "version\t%s\n", v.InstalledVersion)
	_, _ = fmt.Fprintf(tw, "enabled\t%t\n", v.Enabled)
	_, _ = fmt.Fprintf(tw, "runtime\t%s\n", v.RuntimeMode)
	_, _ = fmt.Fprintf(tw, "grants\t%s\n", strings.Join(v.Grants, ","))
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// withPluginStore opens the embedded plugin store, runs fn, and always
// closes it — the one place every read/toggle verb funnels its storage
// access through, so a leaked handle can never happen in one command and
// not another.
func withPluginStore[T any](ctx context.Context, deps pluginDeps, fn func(context.Context, provider.Store) (T, error)) (T, error) {
	var zero T
	store, closeStore, err := openPluginStore(ctx, deps.Paths, deps.Clock)
	if err != nil {
		return zero, err
	}
	defer closeStore()
	return fn(ctx, store)
}
