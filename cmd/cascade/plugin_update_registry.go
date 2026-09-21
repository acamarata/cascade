// Purpose: the X/S-50.T8 registry-driven half of `cascade plugin update
// [name]` (no --from): CheckUpdate against the signed index (X/S-50.T1),
// c.verifier.VerifyArtifact-before-grant-diff (checksum AND signature —
// X/S-50.T6's registry client, via StageAndVerifyArtifact), a grant-diff
// re-confirmation layer, and the --yes/--accept-grant non-interactive
// consent flags. Split from plugin_update.go purely to stay under
// Art.10.3's 300-line cap — plugin_registry_client.go/plugin_search.go's
// own established split precedent (S-50.T2).
//
// S-50.T8 REWORK (adversarial CR verdict, /tmp/cascade-evidence/
// s50t8-cr-verdict.txt, FIX-1/2/3): an earlier revision of this file
// committed grant-expanding updates directly via plugins.SaveMetadata,
// bypassing the O/S-32.T4 elevation gate (internal/plugins.UpdatePlugin)
// entirely — a signed registry entry could move a sandboxed plugin into a
// process-tier runtime, or add capabilities, with zero elevation on a
// daemonless host. That second commit path is GONE: this file now calls
// plugins.UpdatePlugin — the SAME function --from's own branch in
// plugin_update.go calls, and the SAME result rendering
// (renderPluginUpdateResult) — for every registry-driven update, with no
// exception. --accept-grant=<grant> is the operator's explicit
// acknowledgement that a grant expansion was reviewed and is REQUIRED
// before this file will even attempt the update (confirmPluginUpdateGrants
// below), but it is NEVER the authority that lets an expansion (or a
// runtime-tier change) commit: with no daemon, UpdatePlugin itself refuses
// with the typed ErrDaemonRequiredForElevatedPluginOp regardless of
// --accept-grant; with a daemon, UpdatePlugin's own elevation outcome
// (UpdateOutcomeElevationRequired — this build has no attestation source
// yet) is surfaced verbatim by renderPluginUpdateResult, not silently
// bypassed. The registry path runs no ProcessHandshaker (a real, disclosed
// gap — see the journal — distinct from --from's O/S-31.T3 handshake,
// which needs a running child process this path has no daemon-mediated
// way to launch yet).
//
// SPORT: cli/plugin-lifecycle/CHANGE (P1-E24-W5-S50-T8, reworked).
package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// errRegistryUpdateDaemonConfigured mirrors plugin_search.go's D9
// discipline: a daemon CONFIGURED for this process must never be
// silently bypassed by a direct local network call. No "plugin.update"
// registry-check RPC method exists yet to route through instead (a real,
// disclosed gap — not a silent fallback).
var errRegistryUpdateDaemonConfigured = cascade.New(cascade.KindUnavailable,
	"cascade plugin update: a daemon is configured for this host; the registry-mediated update check does not "+
		"route through it yet (no plugin.update RPC method exists) and refuses to bypass it with a direct "+
		"network call — update via --from, or run without a configured daemon")

// errRegistryNotConfiguredForUpdate is returned when no daemon is
// configured AND [registry].url itself resolves to nothing
// (buildPluginRegistryClient's own "not configured" state): `plugin
// update` without --from has no candidate source at all in that case.
var errRegistryNotConfiguredForUpdate = cascade.New(cascade.KindInvalidInput,
	"cascade plugin update: no --from manifest given and [registry].url is not configured; "+
		"either pass --from <path> or configure [registry] in config.toml")

// productionUpdateRegistryClient is deps.UpdateRegistryClient's real
// implementation.
func productionUpdateRegistryClient(deps pluginDeps) func(context.Context) (*plugin.RegistryClient, error) {
	return func(ctx context.Context) (*plugin.RegistryClient, error) {
		if pluginDaemonConfigured(ctx, deps.Paths) {
			return nil, errRegistryUpdateDaemonConfigured
		}
		client, warning := buildPluginRegistryClient(ctx, deps.Paths, deps.Clock)
		if warning != "" {
			slog.Default().Warn(warning)
		}
		if client == nil {
			return nil, errRegistryNotConfiguredForUpdate
		}
		return client, nil
	}
}

// resolveUpdateRegistryClient resolves the *plugin.RegistryClient this
// invocation uses: deps.UpdateRegistryClient when a test injected one,
// productionUpdateRegistryClient otherwise.
func resolveUpdateRegistryClient(ctx context.Context, deps pluginDeps) (*plugin.RegistryClient, error) {
	buildClient := deps.UpdateRegistryClient
	if buildClient == nil {
		buildClient = productionUpdateRegistryClient(deps)
	}
	return buildClient(ctx)
}

// loadInstalledForUpdate loads name's metadata record, refusing with
// KindNotFound when it does not exist — `update` presumes a prior
// `plugin add`; a name with no record is never silently treated as a
// fresh install here (adversarial CR FIX-7: the dead
// existing.InstalledVersion-is-empty branch this replaces implicitly did).
func loadInstalledForUpdate(ctx context.Context, deps pluginDeps, name string) (plugins.PluginMetadata, error) {
	type result struct {
		rec plugins.PluginMetadata
		ok  bool
	}
	r, err := withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (result, error) {
		rec, ok, err := plugins.LoadMetadata(ctx, store, name)
		return result{rec, ok}, err
	})
	if err != nil {
		return plugins.PluginMetadata{}, err
	}
	if !r.ok {
		return plugins.PluginMetadata{}, cascade.Newf(cascade.KindNotFound, "cascade plugin update: %q is not installed", name)
	}
	return r.rec, nil
}

// runPluginUpdateFromRegistry is `cascade plugin update <name>` (no
// --from): CheckUpdate → (nil ⇒ "no update available", exit 0) →
// resolveVerifiedCandidate (StageAndVerifyArtifact → ParseManifest,
// checksum AND signature both real) → confirmPluginUpdateGrants (grant
// diff shown, --accept-grant required for an expansion) →
// plugins.UpdatePlugin (the ONE commit path, elevation-gated) →
// renderPluginUpdateResult.
func runPluginUpdateFromRegistry(cmd *cobra.Command, deps pluginDeps, name string, yes bool, acceptGrant []string) error {
	ctx := cmd.Context()
	client, err := resolveUpdateRegistryClient(ctx, deps)
	if err != nil {
		return err
	}
	existing, err := loadInstalledForUpdate(ctx, deps, name)
	if err != nil {
		return err
	}

	entry, err := client.CheckUpdate(ctx, name, existing.InstalledVersion)
	if err != nil {
		return err
	}
	if entry == nil {
		return pluginOutputWriter(cmd).Result(pluginUpdateView{Name: name, Message: "no update available"})
	}

	artifact, m, err := resolveVerifiedCandidate(ctx, deps, client, name, *entry)
	if err != nil {
		return err
	}
	if err := confirmPluginUpdateGrants(cmd, deps, m, existing, yes, acceptGrant); err != nil {
		return err
	}

	res, err := withPluginStore(ctx, deps, func(ctx context.Context, store provider.Store) (plugins.UpdateResult, error) {
		return plugins.UpdatePlugin(ctx, store, nil, nil, artifact, entry.Checksum, artifact, pluginDaemonAvailable(ctx))
	})
	if err != nil {
		return err
	}
	return renderPluginUpdateResult(cmd, deps, res)
}

// resolveVerifiedCandidate downloads and fully verifies entry's artifact
// (checksum AND signature, staging cleaned up either way —
// StageAndVerifyArtifact's own guarantee), parses it as a manifest, and
// confirms both its ID and its VERSION match what was requested/selected
// before any grant-diff or store write ever sees it (adversarial CR
// FIX-6: the version check is new — a registry that named one version in
// its index entry but served different artifact bytes under a different
// version string inside the manifest itself must not go undetected).
func resolveVerifiedCandidate(ctx context.Context, deps pluginDeps, client *plugin.RegistryClient, name string, entry plugin.RegistryVersionEntry) ([]byte, plugin.Manifest, error) {
	stagingDir := filepath.Join(deps.Paths.DataDir(), "plugin-update-staging")
	artifact, err := client.StageAndVerifyArtifact(ctx, entry, stagingDir)
	if err != nil {
		return nil, plugin.Manifest{}, err
	}
	m, err := plugin.ParseManifest(bytes.NewReader(artifact))
	if err != nil {
		return nil, plugin.Manifest{}, err
	}
	if m.ID != name {
		return nil, plugin.Manifest{}, cascade.Newf(cascade.KindIntegrity,
			"cascade plugin update: candidate manifest id %q does not match requested name %q", m.ID, name)
	}
	if m.Version != entry.Version {
		return nil, plugin.Manifest{}, cascade.Newf(cascade.KindIntegrity,
			"cascade plugin update: candidate manifest version %q does not match registry entry version %q", m.Version, entry.Version)
	}
	return artifact, m, nil
}

// confirmPluginUpdateGrants presents m's grant diff against existing
// whenever there IS one — added, removed, or both (adversarial CR FIX-5:
// an earlier revision returned before rendering anything when added was
// empty, so a removal-only diff was never shown even though
// pluginUpdateGrantDiffView already knew how to render one). §5.8's
// consent gate (CASCADE_NO_INPUT=1 without --yes refuses; --accept-grant
// coverage required) applies ONLY when added is non-empty — removing a
// capability is never itself a would-prompt moment.
func confirmPluginUpdateGrants(cmd *cobra.Command, deps pluginDeps, m plugin.Manifest, existing plugins.PluginMetadata, yes bool, acceptGrant []string) error {
	added, removed := plugin.GrantDiff(existing.Grants, m.Requires)
	if len(added) == 0 && len(removed) == 0 {
		return nil // identical grant set -> no diff to show, proceed directly.
	}
	if len(added) > 0 && !yes && pluginNoInput(deps) {
		return plugins.ErrNoInputHardError("update")
	}
	_ = pluginOutputWriter(cmd).Result(pluginUpdateGrantDiffView{Name: m.ID, Added: added, Removed: removed})
	return plugin.ConfirmGrantAcceptance(added, acceptGrant)
}

// pluginUpdateGrantDiffView is the grant-diff re-confirmation display
// (06-FORGE-SPEC §5.8/§5.9): added/removed capability names presented
// before ConfirmGrantAcceptance decides whether the update may proceed.
type pluginUpdateGrantDiffView struct {
	Name    string   `json:"name"`
	Added   []string `json:"added_grants"`
	Removed []string `json:"removed_grants,omitempty"`
}

func (v pluginUpdateGrantDiffView) String() string {
	var parts []string
	if len(v.Added) > 0 {
		parts = append(parts, v.Name+" requests new capabilities: "+strings.Join(v.Added, ", "))
	}
	if len(v.Removed) > 0 {
		parts = append(parts, v.Name+" no longer requires: "+strings.Join(v.Removed, ", "))
	}
	return strings.Join(parts, "; ")
}
