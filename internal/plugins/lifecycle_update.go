// Package plugins (lifecycle_update.go): Purpose: `cascade plugin update`'s three-phase business logic: an
// allowed-fail registry version check (§5.19, ratified fourth leg
// R-16.41), checksum-pin verify, and the grant-expansion elevation
// decision, plus O/S-31.T3 handshake-failure rollback. Registry fetch,
// artifact download and process handshake are all injected seams (this
// file imports pkg/** only — see lifecycle.go's package doc for why); the
// real adapters live in cmd/cascade/plugin.go.
//
// SPORT: internal/plugins lifecycle-update/ADD — P1-E15-W4-S32-T4.
package plugins

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// RegistryVersionChecker is the local seam for the §5.19 allowed-fail
// registry check. A nil RegistryVersionChecker (the honest state of this
// tree today: X/S-50.T1's live HTTP registry client does not exist yet —
// pkg/plugin.RegistryClient's own doc comment says it "declares the seam's
// data shapes and interfaces only") is treated exactly like a checker that
// always fails: UpdatePlugin never distinguishes "no checker configured"
// from "checker call failed," so wiring a real one later changes no
// caller-visible behavior beyond making the check actually able to
// succeed.
type RegistryVersionChecker interface {
	// LatestVersion reports the newest published version for name, or an
	// error if the registry cannot be reached/verified.
	LatestVersion(ctx context.Context, name string) (version string, err error)
}

// ProcessHandshaker is the local seam for O/S-31.T3's launch+handshake
// step an update must confirm before committing. Returning a non-nil error
// is the rollback trigger.
type ProcessHandshaker interface {
	// Handshake attempts to launch and confirm the given plugin/version;
	// a non-nil error means the new bundle failed to come up cleanly.
	Handshake(ctx context.Context, name, version string) error
}

// UpdateOutcome discriminates UpdatePlugin's terminal states.
type UpdateOutcome int

const (
	// UpdateOutcomeUpdated reports a successful update: checksum verified,
	// handshake (when checker present) succeeded, metadata record written.
	UpdateOutcomeUpdated UpdateOutcome = iota
	// UpdateOutcomeElevationRequired reports a grant-expansion that needs
	// elevation before this update can be applied. No write happened.
	UpdateOutcomeElevationRequired
	// UpdateOutcomeRolledBack reports that the handshake failed after
	// checksum verify passed; the prior metadata record was restored
	// unchanged (it was never overwritten in the first place — see
	// UpdatePlugin's doc comment on why this is a true rollback rather
	// than a restore-from-backup).
	UpdateOutcomeRolledBack
)

// UpdateResult is UpdatePlugin's full answer.
type UpdateResult struct {
	Outcome UpdateOutcome
	// RegistryNotice is a non-empty, user-facing graceful notice exactly
	// when the registry check could not run/complete — never an error,
	// per §5.19.
	RegistryNotice string
	Manifest       plugin.Manifest
	Metadata       PluginMetadata
	// RollbackErr carries the handshake failure that triggered
	// UpdateOutcomeRolledBack, for display.
	RollbackErr error
}

// UpdatePlugin runs the three-phase update flow against name's current
// install. manifestSource/checksum/artifact describe the CANDIDATE new
// version, exactly like AddPlugin's equivalent parameters. registry and
// handshaker may be nil (see their doc comments for the resulting
// behavior). daemonAvailable is AddPlugin's identical parameter: when the
// candidate manifest requires elevation — a grant-set expansion OR a
// runtime-tier change, per updateRequiresElevation — and daemonAvailable
// is false, UpdatePlugin refuses with ErrDaemonRequiredForElevatedPluginOp
// before touching the store (D/S-07.T4). This is the ONE commit path
// every caller — the O/S-32.T4 --from flow and the X/S-50.T8 registry-
// driven flow alike — funnels through; neither caller ever writes a
// PluginMetadata record itself (S-50.T8 rework, adversarial CR FIX-1/2).
//
// Fail-closed guards (each refuses before any write): a remote-runtime
// candidate is KindUnsupported (DEF-P2-remote-plugin-runtime); a name with
// no stored record is KindNotFound (an update never installs; that is
// `cascade plugin add`); and commitUpdate refuses a process-tier candidate
// it cannot handshake.
//
// Rollback mechanics: the new PluginMetadata record is committed ONLY after
// handshaker.Handshake succeeds. A nil handshaker is accepted only for a
// non-process runtime, which has no child process to confirm. The prior
// record is therefore never touched by a failing update: "rollback" is
// "never having applied the change," which needs no snapshot step.
func UpdatePlugin(ctx context.Context, store provider.Store, registry RegistryVersionChecker, handshaker ProcessHandshaker, manifestSource []byte, checksum string, artifact []byte, daemonAvailable bool) (UpdateResult, error) {
	m, err := parseUpdatableManifest(manifestSource)
	if err != nil {
		return UpdateResult{}, err
	}

	notice := checkRegistryGraceful(ctx, registry, m.ID)

	if err := verifyUpdateChecksum(checksum, artifact); err != nil {
		return UpdateResult{}, err
	}

	existing, err := loadInstalled(ctx, store, m.ID)
	if err != nil {
		return UpdateResult{}, err
	}

	if updateRequiresElevation(m, existing) {
		if !daemonAvailable {
			return UpdateResult{}, ErrDaemonRequiredForElevatedPluginOp("update")
		}
		return UpdateResult{Outcome: UpdateOutcomeElevationRequired, RegistryNotice: notice, Manifest: m}, nil
	}
	return commitUpdate(ctx, store, handshaker, m, existing, checksum, notice)
}

// parseUpdatableManifest parses a candidate manifest and refuses the
// deferred remote runtime before anything else looks at it.
func parseUpdatableManifest(manifestSource []byte) (plugin.Manifest, error) {
	m, err := plugin.ParseManifest(bytes.NewReader(manifestSource))
	if err != nil {
		return plugin.Manifest{}, err
	}
	if err := RefuseRemoteRuntime("update", m); err != nil {
		return plugin.Manifest{}, err
	}
	return m, nil
}

// verifyUpdateChecksum checks artifact against a pinned checksum; an empty
// checksum skips the check (AddPlugin's identical rule).
func verifyUpdateChecksum(checksum string, artifact []byte) error {
	if checksum == "" {
		return nil
	}
	return plugin.VerifyArtifact(plugin.RegistryVersionEntry{Checksum: checksum}, artifact)
}

// loadInstalled returns id's stored record, or KindNotFound when there is
// none: an update presumes a prior `cascade plugin add`.
func loadInstalled(ctx context.Context, store provider.Store, id string) (PluginMetadata, error) {
	existing, ok, err := LoadMetadata(ctx, store, id)
	if err != nil {
		return PluginMetadata{}, err
	}
	if !ok {
		return PluginMetadata{}, cascade.Newf(cascade.KindNotFound, "%s is not installed; use cascade plugin add", id)
	}
	return existing, nil
}

// errProcessHandshakeUnconfirmed is the refusal for a process-tier update
// with no handshaker to confirm the new bundle comes up.
var errProcessHandshakeUnconfirmed = cascade.New(cascade.KindUnavailable,
	"cannot confirm the process handshake; update not applied")

// commitUpdate is the one commit step every update path shares, the CLI's
// non-elevated path and the daemon's attested plugin.update alike: the
// process-tier handshake guard, the handshake itself, then the write.
func commitUpdate(ctx context.Context, store provider.Store, handshaker ProcessHandshaker, m plugin.Manifest, existing PluginMetadata, checksum, notice string) (UpdateResult, error) {
	if handshaker == nil && m.Runtime == plugin.RuntimeProcess {
		return UpdateResult{}, errProcessHandshakeUnconfirmed
	}
	if handshaker != nil {
		if hsErr := handshaker.Handshake(ctx, m.ID, m.Version); hsErr != nil {
			return UpdateResult{Outcome: UpdateOutcomeRolledBack, RegistryNotice: notice, Manifest: m, Metadata: existing, RollbackErr: hsErr}, nil
		}
	}
	rec := PluginMetadata{
		Name:             m.ID,
		InstalledVersion: m.Version,
		Enabled:          existing.Enabled,
		RuntimeMode:      m.Runtime,
		PinnedChecksum:   checksum,
		Grants:           append([]string(nil), m.Requires...),
	}
	if err := SaveMetadata(ctx, store, rec); err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Outcome: UpdateOutcomeUpdated, RegistryNotice: notice, Manifest: m, Metadata: rec}, nil
}

// updateRequiresElevation is the update-side counterpart to
// lifecycle_add.go's addRequiresElevation: ANY change to RuntimeMode (not
// only a move to RuntimeProcess) is elevated, because the installed tier is
// a security boundary an operator chose, and so is a grant expansion.
// existing is always a real stored record here (loadInstalled refuses
// otherwise), so the tier comparison never runs against a zero value.
func updateRequiresElevation(m plugin.Manifest, existing PluginMetadata) bool {
	if m.Runtime != existing.RuntimeMode {
		return true
	}
	return grantsExpand(existing.Grants, m.Requires)
}

// registryUnavailableNotice is the exact §5.19 graceful-notice text.
const registryUnavailableNotice = "registry unavailable — proceeding with local verification only"

// checkRegistryGraceful runs the allowed-fail leg: a nil checker or any
// checker error both produce the graceful notice and a non-erroring
// return; only a checker that succeeds AND names a newer version could, in
// a fuller implementation, change control flow — this ticket's own scope
// is "attempted and allowed to fail gracefully" (X/S-50.T8 carries the
// end-to-end integration test for the success path), so a successful
// check's version is not otherwise consulted here.
func checkRegistryGraceful(ctx context.Context, registry RegistryVersionChecker, name string) string {
	if registry == nil {
		return registryUnavailableNotice
	}
	if _, err := registry.LatestVersion(ctx, name); err != nil {
		return registryUnavailableNotice
	}
	return ""
}
