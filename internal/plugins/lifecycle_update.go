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
// Rollback mechanics: this function commits the new PluginMetadata record
// ONLY after handshaker.Handshake succeeds (or handshaker is nil, in which
// case there is nothing to roll back FROM — see cmd/cascade/plugin.go for
// why a nil handshaker is refused before this function is ever called in
// production). The prior record is therefore never touched by a failing
// update: "rollback" is "never having applied the change," which is a
// correct and stronger guarantee than restore-from-backup, and needs no
// separate snapshot step.
func UpdatePlugin(ctx context.Context, store provider.Store, registry RegistryVersionChecker, handshaker ProcessHandshaker, manifestSource []byte, checksum string, artifact []byte, daemonAvailable bool) (UpdateResult, error) {
	m, err := plugin.ParseManifest(bytes.NewReader(manifestSource))
	if err != nil {
		return UpdateResult{}, err
	}

	notice := checkRegistryGraceful(ctx, registry, m.ID)

	if checksum != "" {
		entry := plugin.RegistryVersionEntry{Checksum: checksum}
		if err := plugin.VerifyArtifact(entry, artifact); err != nil {
			return UpdateResult{}, err
		}
	}

	existing, existingOK, err := LoadMetadata(ctx, store, m.ID)
	if err != nil {
		return UpdateResult{}, err
	}

	if updateRequiresElevation(m, existing, existingOK) {
		if !daemonAvailable {
			return UpdateResult{}, ErrDaemonRequiredForElevatedPluginOp("update")
		}
		return UpdateResult{Outcome: UpdateOutcomeElevationRequired, RegistryNotice: notice, Manifest: m}, nil
	}

	if handshaker != nil {
		if hsErr := handshaker.Handshake(ctx, m.ID, m.Version); hsErr != nil {
			return UpdateResult{Outcome: UpdateOutcomeRolledBack, RegistryNotice: notice, Manifest: m, Metadata: existing, RollbackErr: hsErr}, nil
		}
	}

	rec := PluginMetadata{
		Name:             m.ID,
		InstalledVersion: m.Version,
		Enabled:          existing.Enabled || existing.InstalledVersion == "",
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
// lifecycle_add.go's addRequiresElevation, extended by the S-50.T8 rework
// pass's own bug fix (adversarial CR FIX-3, PCI filed against this file):
// the ORIGINAL check here consulted grantsExpand alone, unlike
// addRequiresElevation, which also treats a RuntimeMode change as
// elevated. That asymmetry let a candidate manifest change an EXISTING
// installed plugin's runtime tier (builtin, process, wasm, or remote —
// not merely "to process") with NO grant expansion at all, on a host with
// no daemon configured, and never trigger elevation: a signed registry
// entry could move a sandboxed plugin into a supervised child process (or
// any other tier) with zero consent. ANY change to RuntimeMode — not only
// a move specifically TO RuntimeProcess — is elevated when existingOK: the
// installed tier is itself a security boundary an operator chose, and no
// tier change is a silent, unconsented step. existingOK gates the
// comparison to an ACTUAL prior install (LoadMetadata's own ok result):
// with no prior record, existing's zero-valued RuntimeMode is not a real
// tier to compare against, so only grantsExpand's own "any non-empty
// Requires on a fresh record is itself an expansion" rule applies — the
// identical, pre-existing behavior for that path (see
// TestUpdatePluginPropagatesStoreFailures, which calls UpdatePlugin
// against an empty store on purpose).
func updateRequiresElevation(m plugin.Manifest, existing PluginMetadata, existingOK bool) bool {
	if existingOK && m.Runtime != existing.RuntimeMode {
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
