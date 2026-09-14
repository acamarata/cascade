// Purpose: `cascade plugin add <ref> [--checksum <hash>]`. ref is a local
// path to a cascade.plugin/v2 manifest — the contract's "registry hint"
// half belongs to X/S-50 (`cascade plugin search`), explicitly out of
// scope here per the contract's own first line.
//
// Elevation: internal/plugins.AddPlugin decides whether the manifest needs
// elevation (process-tier runtime, or a grant-expansion) and, when it does,
// refuses outright unless a daemon is present (D/S-07.T4). This file's job
// is the CLI-side consent display the contract asks for, never the
// elevation middleware itself ("the elevation middleware is D/S-06.T3's,
// not reimplemented here" — contract, verbatim). Once daemon-availability
// is confirmed, the actual elevated request routes through the D/S-07.T3
// client SDK (pluginClient) to a "plugin.add" RPC method; no daemon-side
// handler for that method exists yet in this tree (S32-T3's journal
// recorded the identical gap: "No new CLI surface. No new JSON-RPC method
// registration" was out of ITS scope, and registering one is out of this
// ticket's files_scope too — internal/rpc/*.go is not listed). A real
// daemon therefore answers "method not found" today; this file's own
// responsibility (the local decision, the refusal, and the wire call it
// is correct to attempt) is complete and tested independent of that gap.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

import (
	"bytes"
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// newPluginAddCmd builds `plugin add`.
func newPluginAddCmd(deps pluginDeps) *cobra.Command {
	var checksum string
	cmd := &cobra.Command{
		Use:   "add <ref>",
		Short: "Install a plugin from a local manifest path (elevated when process-tier or grant-expanding)",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginAdd(cmd, deps, args[0], checksum)
		},
	}
	cmd.Flags().StringVar(&checksum, "checksum", "", "hex SHA-256 the local bundle must match before it is trusted")
	return cmd
}

func runPluginAdd(cmd *cobra.Command, deps pluginDeps, ref, checksum string) error {
	manifestBytes, err := os.ReadFile(ref)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "cascade plugin add: read manifest %q", ref)
	}

	// A cheap, side-effect-free pre-parse just to know whether THIS
	// manifest is process-tier, so a Windows caller gets the exact §Art.5
	// wording before AddPlugin's own (platform-agnostic) daemon check
	// would otherwise produce the generic daemon-required message. A
	// grant-expansion-only elevation (builtin/wasm tier requesting new
	// capabilities) is not distinguishable this cheaply without also
	// loading the existing record, and falls through to AddPlugin's own
	// refusal — a narrower, documented gap (see this file's header).
	if m, perr := plugin.ParseManifest(bytes.NewReader(manifestBytes)); perr == nil && m.Runtime == plugin.RuntimeProcess {
		if err := pluginWindowsTier2Refusal("daemon"); err != nil {
			return err
		}
	}

	ctx := cmd.Context()
	daemonAvailable := pluginDaemonAvailable(ctx)

	res, err := withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (plugins.AddResult, error) {
		return plugins.AddPlugin(ctx, store, manifestBytes, checksum, manifestBytes, daemonAvailable)
	})
	if err != nil {
		return err
	}

	switch res.Outcome {
	case plugins.AddOutcomeAlreadyInstalled:
		return pluginOutputWriter(cmd).Result(pluginAddView{Message: plugins.AlreadyInstalledMessage(res.Metadata.Name, res.Metadata.InstalledVersion)})
	case plugins.AddOutcomeElevationRequired:
		return runPluginAddElevated(cmd, deps, res.Manifest, checksum)
	case plugins.AddOutcomeInstalled:
		return pluginOutputWriter(cmd).Result(pluginAddView{
			Message: "installed " + res.Metadata.Name + " v" + res.Metadata.InstalledVersion,
			Grants:  res.Metadata.Grants,
		})
	default:
		return cascade.Newf(cascade.KindInternal, "cascade plugin add: unrecognized outcome %d", res.Outcome)
	}
}

// runPluginAddElevated displays the requested grant set and, unless
// CASCADE_NO_INPUT=1 (§5.8, hard error, never a silent default), routes
// the elevated request to the daemon via the client SDK.
func runPluginAddElevated(cmd *cobra.Command, deps pluginDeps, m plugin.Manifest, checksum string) error {
	if pluginNoInput(deps) {
		return plugins.ErrNoInputHardError("add")
	}
	w := pluginOutputWriter(cmd)
	_ = w.Result(pluginAddView{
		Message: "cascade plugin add: " + m.ID + " requests runtime=" + string(m.Runtime) + " and capabilities: " + joinOrNone(m.Requires),
		Grants:  m.Requires,
	})

	c, err := pluginClient(deps)
	if err != nil {
		return err
	}
	var result pluginAddView
	if err := c.Do(cmd.Context(), "plugin.add", pluginAddParams{ID: m.ID, Checksum: checksum}, &result); err != nil {
		return err
	}
	return w.Result(result)
}

type pluginAddParams struct {
	ID       string `json:"id"`
	Checksum string `json:"checksum,omitempty"`
}

type pluginAddView struct {
	Message string   `json:"message"`
	Grants  []string `json:"grants,omitempty"`
}

func (v pluginAddView) String() string { return v.Message }

func joinOrNone(xs []string) string {
	if len(xs) == 0 {
		return "(none)"
	}
	out := xs[0]
	for _, x := range xs[1:] {
		out += ", " + x
	}
	return out
}
