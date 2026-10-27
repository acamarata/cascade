// Purpose: `cascade plugin perms grant|revoke <name> <capability>`. Both
// directions are unconditionally elevated (§5.14). The change is made by the
// daemon's attested perms.grant / perms.revoke (internal/plugins
// perms_rpc.go), because the daemon holds the plugin store; without a daemon
// the command refuses, as does CASCADE_NO_INPUT=1, in that order, and both
// refusals are recorded as attempt then deny.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
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
	verb, label := plugins.PermsRevokeRPCMethod, "perms revoke"
	if grant {
		verb, label = plugins.PermsGrantRPCMethod, "perms grant"
	}
	// The daemon holds the plugin store, so a change is always the daemon's
	// attested perms.* call; without a daemon nothing can apply it. Both
	// refusals are recorded as attempt then deny.
	if !daemonAvailable {
		return recordPluginRefusal(ctx, deps, verb, "daemon_required", plugins.ErrDaemonRequiredForElevatedPluginOp(label))
	}
	if pluginNoInput(deps) {
		return recordPluginRefusal(ctx, deps, verb, "no_input", plugins.ErrNoInputHardError(label))
	}
	var res plugins.PermsResult
	if err := pluginPermsElevatedCall(ctx, deps, cmd.ErrOrStderr(), verb, plugins.PermsParams{Name: name, Capability: capability}, &res); err != nil {
		return err
	}
	// The diff is carried in the result: JSON mode never interleaves free
	// text with the envelope (Writer.Println's own doc comment).
	return pluginOutputWriter(cmd).Result(pluginPermsView{Name: res.Name, Before: res.Before, After: res.After})
}

// pluginPermsElevatedCall dials perms.grant / perms.revoke through
// callElevated. Tests replace it to reach an in-process registry.
var pluginPermsElevatedCall = func(ctx context.Context, deps pluginDeps, stderr io.Writer, method string, params plugins.PermsParams, out any) error {
	c, err := pluginClient(deps)
	if err != nil {
		return err
	}
	return callElevated(ctx, c, productionElevatedCallDeps(stderr), method, params, out)
}

type pluginPermsView struct {
	Name   string   `json:"name"`
	Before []string `json:"grants_before"`
	After  []string `json:"grants_after"`
}

func (v pluginPermsView) String() string {
	return v.Name + " grants: " + joinOrNone(v.Before) + " -> " + joinOrNone(v.After)
}
