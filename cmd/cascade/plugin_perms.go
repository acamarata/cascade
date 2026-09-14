// Purpose: `cascade plugin perms grant|revoke <name> <capability>`. Both
// directions are unconditionally elevated (§5.14) — internal/plugins.
// ChangePerms enforces the daemon-required and CASCADE_NO_INPUT gates
// itself (in that order), so this file's job is purely the CLI-side grant-
// diff display before the call and rendering the result after. See
// plugin_add.go's header for the same "no daemon-side handler registered
// yet" gap; the local decision and refusal are complete and tested either
// way.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/provider"
)

// newPluginPermsCmd builds the `plugin perms` group.
func newPluginPermsCmd(deps pluginDeps) *cobra.Command {
	permsCmd := &cobra.Command{
		Use:   "perms",
		Short: "Grant or revoke one capability on an installed plugin (elevated)",
	}
	permsCmd.AddCommand(newPluginPermsChangeCmd(deps, "grant", true))
	permsCmd.AddCommand(newPluginPermsChangeCmd(deps, "revoke", false))
	return permsCmd
}

func newPluginPermsChangeCmd(deps pluginDeps, verb string, grant bool) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <name> <capability>",
		Short: "Elevated: change one capability grant on an installed plugin",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginPerms(cmd, deps, args[0], args[1], grant)
		},
	}
}

func runPluginPerms(cmd *cobra.Command, deps pluginDeps, name, capability string, grant bool) error {
	ctx := cmd.Context()
	daemonAvailable := pluginDaemonAvailable(ctx)
	if err := pluginWindowsTier2Refusal("elevation"); err != nil {
		return err
	}

	// The contract asks the diff to be displayed before it applies; since
	// JSON mode never interleaves free text with the envelope (Writer.
	// Println's own doc comment), the diff is carried IN the result
	// instead — visible in both modes, and additionally asserted by a
	// test without needing to scrape stderr.
	before, _ := withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (plugins.PluginMetadata, error) {
		rec, _, err := plugins.LoadMetadata(ctx, store, name)
		return rec, err
	})

	rec, err := withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (plugins.PluginMetadata, error) {
		return plugins.ChangePerms(ctx, store, name, capability, grant, daemonAvailable, pluginNoInput(deps))
	})
	if err != nil {
		return err
	}
	return pluginOutputWriter(cmd).Result(pluginPermsView{Name: rec.Name, Before: before.Grants, After: rec.Grants})
}

type pluginPermsView struct {
	Name   string   `json:"name"`
	Before []string `json:"grants_before"`
	After  []string `json:"grants_after"`
}

func (v pluginPermsView) String() string {
	return v.Name + " grants: " + joinOrNone(v.Before) + " -> " + joinOrNone(v.After)
}
