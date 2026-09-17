// Purpose: `cascade plugin enable/disable/remove` — the three verbs §5.14
// never elevates. All three run entirely against the embedded store
// (plugin.go's withPluginStore); none dials the daemon.
//
// Inputs/Outputs/Constraints: see plugin.go's package doc.
//
// Known gap (honest, not silently dropped): the contract's "trigger daemon
// host-state reload" for enable/disable, and remove's O/S-31.T3 drain of a
// LIVE in-daemon process handle, both need a running daemon subsystem this
// ticket's files_scope cannot reach (no internal/rpc or internal/daemon
// change is in scope — S32-T3's own journal recorded the identical
// out-of-scope boundary: "No new CLI surface. No new JSON-RPC method
// registration"). What lands here is the real, complete metadata-record
// change (the durable state these verbs exist to control) with a nil
// ProcessTeardown for remove — RemovePlugin's own doc comment names nil as
// "nothing to drain," the honest answer for a one-shot CLI process that
// never held the plugin's live handle in the first place.
//
// UPDATE (P1-E16-W4-S34-T1): remove no longer passes nil. Draining a live
// in-daemon handle is still out of reach here, but cascade-claude's
// UNINSTALL is not — it is filesystem work a one-shot CLI process can do,
// and leaving it undone meant removing that plugin deleted its record and
// left every file it had written on disk. claudeTeardown below is that
// hook; it declines by name for every other plugin.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	claude "github.com/acamarata/cascade/plugins/claude"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/provider"
)

// newPluginEnableCmd builds `plugin enable <name>`.
func newPluginEnableCmd(deps pluginDeps) *cobra.Command {
	return newPluginToggleCmd(deps, "enable", true)
}

// newPluginDisableCmd builds `plugin disable <name>`.
func newPluginDisableCmd(deps pluginDeps) *cobra.Command {
	return newPluginToggleCmd(deps, "disable", false)
}

func newPluginToggleCmd(deps pluginDeps, verb string, enabled bool) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <name>",
		Short: "Toggle whether an installed plugin is active",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A builtin is refused with what it IS, not with "not
			// installed" — which was false, and which an operator could
			// act on by trying to install something already running.
			if isBuiltinPlugin(args[0]) {
				return errBuiltinNotToggleable(verb, args[0])
			}
			rec, err := withPluginStore(cmd.Context(), deps, func(ctx context.Context, store provider.Store) (plugins.PluginMetadata, error) {
				return plugins.SetEnabled(ctx, store, args[0], enabled)
			})
			if err != nil {
				return err
			}
			return pluginOutputWriter(cmd).Result(pluginToggleView{Name: rec.Name, Enabled: rec.Enabled})
		},
	}
}

// pluginToggleView reports the state a toggle left the record in, per
// LANE-RULES §4: assert a real returned result, not that a call happened.
type pluginToggleView struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (v pluginToggleView) String() string {
	state := "disabled"
	if v.Enabled {
		state = "enabled"
	}
	return v.Name + " is now " + state
}

// newPluginRemoveCmd builds `plugin remove <name>`.
func newPluginRemoveCmd(deps pluginDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Uninstall a plugin: teardown (if running), then delete its record",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if isBuiltinPlugin(args[0]) {
				return errBuiltinNotToggleable("remove", args[0])
			}
			rec, err := withPluginStore(cmd.Context(), deps, func(ctx context.Context, store provider.Store) (plugins.PluginMetadata, error) {
				return plugins.RemovePlugin(ctx, store, claudeTeardown(deps, store), args[0])
			})
			if err != nil {
				return err
			}
			return pluginOutputWriter(cmd).Result(pluginRemoveView{Name: rec.Name, RemovedVersion: rec.InstalledVersion})
		},
	}
}

type pluginRemoveView struct {
	Name           string `json:"name"`
	RemovedVersion string `json:"removed_version"`
}

func (v pluginRemoveView) String() string {
	return "removed " + v.Name + " (was v" + v.RemovedVersion + ")"
}

// claudeTeardown builds the uninstall hook `plugin remove` runs
// (P1-E16-W4-S34-T1). Before this the seam was passed nil, so removing
// cascade-claude deleted its RECORD and left every file it had written on
// disk — an uninstall that uninstalled nothing.
//
// A teardown that cannot be built is nil, which RemovePlugin treats as
// "no hook": the record is still removed. That is the honest degradation.
// Refusing to remove a plugin because its harness paths could not be
// resolved would strand an operator on a machine whose harness is already
// gone, which is exactly when they are most likely to be removing it.
func claudeTeardown(deps pluginDeps, store provider.Store) plugins.ProcessTeardown {
	paths, err := claude.HostPaths()
	if err != nil {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return plugins.NewClaudeTeardown(paths, cwd, audit.New(store, deps.Clock, nil))
}
