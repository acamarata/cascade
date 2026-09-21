// Purpose: `cascade plugin update <name> [--all]`. Two candidate sources,
// selected by whether --from is given, BOTH committing through the exact
// same internal/plugins.UpdatePlugin call and the exact same
// renderPluginUpdateResult below (S-50.T8 rework, adversarial CR FIX-1/2:
// an earlier revision of the registry path committed directly via
// plugins.SaveMetadata, bypassing UpdatePlugin's elevation gate entirely
// — that second commit path is gone): --from <path> reads a local
// candidate manifest (see plugin_add.go's header for the shared "no
// daemon-side handler registered yet" gap); omitting --from routes
// through the X/S-50.T8 registry-driven path instead
// (plugin_update_registry.go): CheckUpdate against the signed index,
// c.verifier.VerifyArtifact (checksum AND signature) before anything else
// touches the candidate, a grant-diff re-confirmation layer, and only
// THEN the identical UpdatePlugin call this file's own --from branch
// makes. --all is unchanged (still refuses — no per-plugin candidate
// source resolution exists for either path).
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4); registry path
// cli/plugin-lifecycle/CHANGE (P1-E24-W5-S50-T8, reworked).
package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// newPluginUpdateCmd builds `plugin update`.
func newPluginUpdateCmd(deps pluginDeps) *cobra.Command {
	var checksum string
	var all bool
	var yes bool
	var acceptGrant []string
	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Update one installed plugin — from the signed registry (default) or a local manifest via --from",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginUpdate(cmd, deps, args, checksum, all, yes, acceptGrant)
		},
	}
	cmd.Flags().StringVar(&checksum, "checksum", "", "hex SHA-256 the candidate bundle must match (--from path only)")
	cmd.Flags().BoolVar(&all, "all", false, "update every installed plugin instead of one name")
	cmd.Flags().String("from", "", "path to a local candidate manifest, bypassing the registry")
	cmd.Flags().BoolVar(&yes, "yes", false, "accept non-interactive defaults (never auto-accepts a grant expansion — see --accept-grant)")
	cmd.Flags().StringArrayVar(&acceptGrant, "accept-grant", nil, "explicitly accept one new capability grant the update requests (repeatable, one per grant)")
	return cmd
}

func runPluginUpdate(cmd *cobra.Command, deps pluginDeps, args []string, checksum string, all, yes bool, acceptGrant []string) error {
	if all {
		return cascade.New(cascade.KindUnsupported,
			"cascade plugin update --all: iterating every installed plugin needs each one's own "+
				"candidate manifest source, which this build resolves only via --from for a single name; "+
				"run `cascade plugin update <name> --from <path>` per plugin")
	}
	if len(args) != 1 {
		return cascade.New(cascade.KindInvalidInput, "cascade plugin update: a plugin name is required without --all")
	}
	from, _ := cmd.Flags().GetString("from")
	if from == "" {
		return runPluginUpdateFromRegistry(cmd, deps, args[0], yes, acceptGrant)
	}
	manifestBytes, err := os.ReadFile(from)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "cascade plugin update: read manifest %q", from)
	}

	ctx := cmd.Context()
	daemonAvailable := pluginDaemonAvailable(ctx)

	res, err := withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (plugins.UpdateResult, error) {
		return plugins.UpdatePlugin(ctx, store, nil, nil, manifestBytes, checksum, manifestBytes, daemonAvailable)
	})
	if err != nil {
		return err
	}
	return renderPluginUpdateResult(cmd, deps, res)
}

// renderPluginUpdateResult maps a plugins.UpdateResult onto pluginUpdateView
// and writes it — the ONE place either candidate source (this file's --from
// path, or plugin_update_registry.go's registry-driven path) turns
// UpdatePlugin's outcome into CLI output, so the two paths can never drift
// on what "elevation required"/"rolled back"/"updated" means to an operator
// (S-50.T8 rework: both paths now share this SAME commit call and this SAME
// result rendering — see plugin_update_registry.go's header for why the
// registry path no longer commits any other way).
func renderPluginUpdateResult(cmd *cobra.Command, deps pluginDeps, res plugins.UpdateResult) error {
	view := pluginUpdateView{Name: res.Manifest.ID, RegistryNotice: res.RegistryNotice}
	switch res.Outcome {
	case plugins.UpdateOutcomeRolledBack:
		view.Message = "update rolled back: " + res.RollbackErr.Error() + " (restored v" + res.Metadata.InstalledVersion + ")"
	case plugins.UpdateOutcomeElevationRequired:
		if pluginNoInput(deps) {
			return plugins.ErrNoInputHardError("update")
		}
		view.Message = res.Manifest.ID + " requests new capabilities: " + joinOrNone(res.Manifest.Requires) + " — elevation required"
	case plugins.UpdateOutcomeUpdated:
		view.Message = "updated " + res.Manifest.ID + " to v" + res.Manifest.Version
	default:
		return cascade.Newf(cascade.KindInternal, "cascade plugin update: unrecognized outcome %d", res.Outcome)
	}
	return pluginOutputWriter(cmd).Result(view)
}

type pluginUpdateView struct {
	Name           string `json:"name"`
	Message        string `json:"message"`
	RegistryNotice string `json:"registry_notice,omitempty"`
}

func (v pluginUpdateView) String() string {
	if v.RegistryNotice != "" {
		return v.Message + " (" + v.RegistryNotice + ")"
	}
	return v.Message
}
