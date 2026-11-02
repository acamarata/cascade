// Purpose: `cascade plugin update <name> --reverify`: re-pin a process
// plugin's executable digest after the executable changed, through the
// attested plugin.update RPC and only against the signed registry index.
// Inputs: the registry client (deps.UpdateRegistryClient or the configured
// one), its verified index bytes, and the installed version's manifest as
// the registry publishes it (StageAndVerifyArtifact: checksum and signature).
// Outputs: one elevated plugin.update call carrying reverify, the index
// bytes, the manifest bytes and the entry checksum; the daemon verifies the
// index with the registry key and re-pins only when the signed exec_sha256
// equals the sibling's digest. The CLI is not a verifier and writes nothing.
// Constraints: needs the daemon (the elevated call has no daemonless path);
// the manifest is the registry's latest published version, which the daemon
// accepts only when it is the installed version.
// SPORT: cli/plugin-lifecycle/CHANGE (P1-PLG-12).
package main

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// runPluginUpdateReverify is the --reverify branch of `plugin update`.
func runPluginUpdateReverify(cmd *cobra.Command, deps pluginDeps, args []string) error {
	for _, f := range []string{"from", "checksum", "accept-grant"} {
		if cmd.Flags().Changed(f) {
			return cascade.Newf(cascade.KindInvalidInput, "cascade plugin update --reverify cannot be combined with --%s", f)
		}
	}
	if all, _ := cmd.Flags().GetBool("all"); all || len(args) != 1 {
		return cascade.New(cascade.KindInvalidInput, "cascade plugin update --reverify needs exactly one plugin name")
	}
	ctx := cmd.Context()
	if pluginNoInput(deps) {
		return recordPluginRefusal(ctx, deps, plugins.UpdateRPCMethod, "no_input", elevation.ErrNoInput())
	}
	if !pluginDaemonAvailable(ctx) {
		return recordPluginRefusal(ctx, deps, plugins.UpdateRPCMethod, "daemon_required", plugins.ErrDaemonRequiredForElevatedPluginOp("update"))
	}
	req, err := buildReverifyRequest(ctx, deps, args[0])
	if err != nil {
		return err
	}
	var out pluginAddView
	if err := pluginUpdateElevatedCall(ctx, deps, cmd.ErrOrStderr(), req, &out); err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind != cascade.KindElevationRequired {
			return err // the daemon's own typed refusal (which link failed)
		}
		return cascade.Wrap(cascade.KindElevationRequired, err, "cascade plugin update --reverify: elevation was not completed; trust is unchanged")
	}
	return pluginOutputWriter(cmd).Result(pluginUpdateView{Name: args[0], Message: out.Message})
}

// buildReverifyRequest fetches the verified index and the registry's
// manifest for name and packs them as the reverify params.
func buildReverifyRequest(ctx context.Context, deps pluginDeps, name string) (plugins.UpdateParams, error) {
	client, err := reverifyRegistryClient(ctx, deps)
	if err != nil {
		return plugins.UpdateParams{}, err
	}
	idx, err := client.Fetch(ctx) // verified with the registry key; the rollback baseline advances
	if err != nil {
		return plugins.UpdateParams{}, err
	}
	index, err := pluginReverifyIndexBytes(ctx, deps)
	if err != nil {
		return plugins.UpdateParams{}, err
	}
	entry, err := latestIndexVersion(idx, name)
	if err != nil {
		return plugins.UpdateParams{}, err
	}
	manifest, err := client.StageAndVerifyArtifact(ctx, entry, filepath.Join(deps.Paths.DataDir(), "plugin-update-staging"))
	if err != nil {
		return plugins.UpdateParams{}, err
	}
	return plugins.UpdateParams{ID: name, Checksum: entry.Checksum, ManifestBytes: manifest, Reverify: true, IndexBytes: index}, nil
}

// reverifyRegistryClient is the injected client, else the configured one;
// unlike the plain registry update it is allowed while a daemon runs,
// because the daemon, not this process, decides.
func reverifyRegistryClient(ctx context.Context, deps pluginDeps) (*plugin.RegistryClient, error) {
	if deps.UpdateRegistryClient != nil {
		return deps.UpdateRegistryClient(ctx)
	}
	client, warning := buildPluginRegistryClient(ctx, deps.Paths, deps.Clock)
	if client == nil {
		if warning != "" {
			return nil, cascade.New(cascade.KindUnavailable, warning)
		}
		return nil, errRegistryNotConfiguredForUpdate
	}
	return client, nil
}

// latestIndexVersion is name's recommended version entry in idx.
func latestIndexVersion(idx plugin.RegistryIndex, name string) (plugin.RegistryVersionEntry, error) {
	for _, e := range idx.Entries {
		if e.ID != name {
			continue
		}
		for _, v := range e.Versions {
			if v.Version == e.LatestVersion {
				return v, nil
			}
		}
	}
	return plugin.RegistryVersionEntry{}, cascade.Newf(cascade.KindNotFound, "cascade plugin update --reverify: %s is not in the signed registry index", name)
}

// pluginReverifyIndexBytes returns the verified raw index the client just
// accepted, from its cache (the RegistryClient exposes only the parsed
// index). The daemon verifies these bytes again; a stale or altered cache
// file cannot pass it. Tests replace it.
var pluginReverifyIndexBytes = func(ctx context.Context, deps pluginDeps) ([]byte, error) {
	dir := filepath.Join(deps.Paths.DataDir(), "plugin-registry-cache")
	if cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: deps.Paths.ConfigPath()}); err == nil && cfg.Registry.CacheDir != "" {
		dir = cfg.Registry.CacheDir
	}
	raw, ok, err := plugin.FileCache{Dir: dir}.Get(ctx, "index")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cascade.New(cascade.KindUnavailable, "cascade plugin update --reverify: the verified registry index is not cached")
	}
	var env struct {
		Raw []byte `json:"raw"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Raw) == 0 {
		return nil, cascade.New(cascade.KindUnavailable, "cascade plugin update --reverify: the cached registry index is unreadable")
	}
	return env.Raw, nil
}
