// Purpose: `cascade plugin update <name> [--all]`. Two candidate sources,
// selected by whether --from is given, BOTH committing through the exact
// same internal/plugins.UpdatePlugin call and the exact same
// renderPluginUpdateResult below (S-50.T8 rework, adversarial CR FIX-1/2:
// an earlier revision of the registry path committed directly via
// plugins.SaveMetadata, bypassing UpdatePlugin's elevation gate entirely
// — that second commit path is gone): --from <path> reads a local
// candidate manifest whose id must equal <name> before any store opens;
// omitting --from routes
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
	"bytes"
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
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
	manifestBytes, m, err := readUpdateCandidate(from, args[0])
	if err != nil {
		return err
	}
	req := plugins.UpdateParams{ID: args[0], Checksum: checksum, ManifestBytes: manifestBytes}

	ctx := cmd.Context()
	if pluginDaemonAvailable(ctx) {
		// The running daemon holds the plugin store exclusively, so the CLI
		// never opens it: the daemon's attested plugin.update re-runs every
		// guard against the stored record and commits. Every daemon-up update
		// therefore needs a signature.
		return runPluginUpdateElevated(cmd, deps, plugins.UpdateResult{Outcome: plugins.UpdateOutcomeElevationRequired, Manifest: m}, req)
	}
	res, err := withElevatedPluginStore(ctx, deps, "plugin.update", func(ctx context.Context, store provider.Store) (plugins.UpdateResult, error) {
		return plugins.UpdatePlugin(ctx, store, nil, nil, manifestBytes, checksum, manifestBytes, false)
	})
	if isDaemonRequired(err, "update") {
		return recordPluginRefusal(ctx, deps, plugins.UpdateRPCMethod, "daemon_required", err)
	}
	if err != nil {
		return err
	}
	return renderPluginUpdateResult(cmd, deps, res, req)
}

// readUpdateCandidate reads the --from manifest and refuses one whose id is
// not name, or whose runtime is the deferred remote runtime, before any
// store is opened or any daemon dialed.
func readUpdateCandidate(from, name string) ([]byte, plugin.Manifest, error) {
	manifestBytes, err := os.ReadFile(from)
	if err != nil {
		return nil, plugin.Manifest{}, cascade.Wrapf(cascade.KindInvalidInput, err, "cascade plugin update: read manifest %q", from)
	}
	m, err := plugin.ParseManifest(bytes.NewReader(manifestBytes))
	if err != nil {
		return nil, plugin.Manifest{}, err
	}
	if m.ID != name {
		return nil, plugin.Manifest{}, cascade.Newf(cascade.KindInvalidInput,
			"cascade plugin update: manifest id %q does not match requested name %q", m.ID, name)
	}
	if err := plugins.RefuseRemoteRuntime("update", m); err != nil {
		return nil, plugin.Manifest{}, err
	}
	return manifestBytes, m, nil
}

// renderPluginUpdateResult maps a plugins.UpdateResult onto pluginUpdateView
// and writes it: the ONE place either candidate source (this file's --from
// path, or plugin_update_registry.go's registry-driven path) turns
// UpdatePlugin's outcome into CLI output. An elevation-required outcome is
// completed only through the daemon's attested plugin.update (req is what
// that call carries); no path writes a record client-side.
func renderPluginUpdateResult(cmd *cobra.Command, deps pluginDeps, res plugins.UpdateResult, req plugins.UpdateParams) error {
	view := pluginUpdateView{Name: res.Manifest.ID, RegistryNotice: res.RegistryNotice}
	switch res.Outcome {
	case plugins.UpdateOutcomeRolledBack:
		view.Message = "update rolled back: " + res.RollbackErr.Error() + " (restored v" + res.Metadata.InstalledVersion + ")"
	case plugins.UpdateOutcomeElevationRequired:
		return runPluginUpdateElevated(cmd, deps, res, req)
	case plugins.UpdateOutcomeUpdated:
		view.Message = "updated " + res.Manifest.ID + " to v" + res.Manifest.Version
	default:
		return cascade.Newf(cascade.KindInternal, "cascade plugin update: unrecognized outcome %d", res.Outcome)
	}
	return pluginOutputWriter(cmd).Result(view)
}

// runPluginUpdateElevated shows what the update requests and completes it
// through plugin.update. CASCADE_NO_INPUT=1 refuses before any dial. A
// typed refusal from the daemon keeps its kind and message; an untyped
// failure (transport, daemon down) or an elevation refusal is a non-zero
// KindElevationRequired exit. Nothing is written client-side either way.
func runPluginUpdateElevated(cmd *cobra.Command, deps pluginDeps, res plugins.UpdateResult, req plugins.UpdateParams) error {
	if pluginNoInput(deps) {
		return recordPluginRefusal(cmd.Context(), deps, plugins.UpdateRPCMethod, "no_input", elevation.ErrNoInput())
	}
	if !pluginDaemonAvailable(cmd.Context()) {
		return recordPluginRefusal(cmd.Context(), deps, plugins.UpdateRPCMethod, "daemon_required", plugins.ErrDaemonRequiredForElevatedPluginOp("update"))
	}
	w := pluginOutputWriter(cmd)
	_ = w.Result(pluginUpdateView{Name: res.Manifest.ID, RegistryNotice: res.RegistryNotice,
		Message: res.Manifest.ID + " requests runtime=" + string(res.Manifest.Runtime) + " and capabilities: " +
			joinOrNone(res.Manifest.Requires) + " — elevation required"})
	var out pluginAddView
	if err := pluginUpdateElevatedCall(cmd.Context(), deps, cmd.ErrOrStderr(), req, &out); err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind != cascade.KindElevationRequired {
			return err // the daemon's own typed refusal (not installed, id mismatch, remote, ...)
		}
		return cascade.Wrap(cascade.KindElevationRequired, err, "cascade plugin update: elevation was not completed; update not applied")
	}
	return w.Result(pluginUpdateView{Name: res.Manifest.ID, Message: out.Message})
}

// pluginUpdateElevatedCall dials plugin.update through callElevated, the one
// elevated client path. Tests replace it to reach an in-process registry.
var pluginUpdateElevatedCall = func(ctx context.Context, deps pluginDeps, stderr io.Writer, req plugins.UpdateParams, out any) error {
	c, err := pluginClient(deps)
	if err != nil {
		return err
	}
	return callElevated(ctx, c, productionElevatedCallDeps(stderr), plugins.UpdateRPCMethod, req, out)
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
