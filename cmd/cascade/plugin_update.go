// Purpose: `cascade plugin update <name> [--all]`. Three-phase flow
// (registry allowed-fail, checksum-pin, grant-expansion elevation) lives in
// internal/plugins.UpdatePlugin; this file resolves the candidate manifest
// path per plugin, calls it, and renders each phase's real outcome
// (registry notice, rollback, or success) rather than a single generic
// "updated" line. See plugin_add.go's header for the shared "no daemon-
// side handler registered yet" gap.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
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
	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Update one (or, with --all, every) installed plugin to the manifest at --from",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginUpdate(cmd, deps, args, checksum, all)
		},
	}
	cmd.Flags().StringVar(&checksum, "checksum", "", "hex SHA-256 the candidate bundle must match")
	cmd.Flags().BoolVar(&all, "all", false, "update every installed plugin instead of one name")
	cmd.Flags().String("from", "", "path to the candidate manifest (required unless --all resolves each plugin's own pinned source)")
	return cmd
}

func runPluginUpdate(cmd *cobra.Command, deps pluginDeps, args []string, checksum string, all bool) error {
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
		return cascade.New(cascade.KindInvalidInput, "cascade plugin update: --from <path> is required")
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
