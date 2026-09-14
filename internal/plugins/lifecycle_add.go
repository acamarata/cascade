// Package plugins (lifecycle_add.go): Purpose: `cascade plugin add`'s business logic: manifest load, checksum
// pin verify, idempotent re-add, and the elevation-required decision
// (§5.14: process-tier runtime or a grant-expansion vs. an existing
// install). This file never prompts, never touches a socket, and never
// starts a process — it decides WHAT should happen; cmd/cascade/plugin.go
// (the real composition root) decides who is allowed to make it happen and
// wires O/S-31.T3's ProcessRuntime for a builtin/wasm-tier install that
// needs no elevation.
//
// SPORT: internal/plugins lifecycle-add/ADD — P1-E15-W4-S32-T4.
package plugins

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// AddOutcome discriminates AddPlugin's three terminal states so the caller
// never has to infer one from field zero-values.
type AddOutcome int

const (
	// AddOutcomeInstalled reports that a new metadata record was written:
	// the plugin was not installed, or installed at a different version.
	AddOutcomeInstalled AddOutcome = iota
	// AddOutcomeAlreadyInstalled reports the §5.9 idempotent case: the
	// requested version was already installed. No write happened.
	AddOutcomeAlreadyInstalled
	// AddOutcomeElevationRequired reports that the manifest requests a
	// process-tier runtime or expands the current grant set, and the
	// caller must obtain elevation before this decision can be applied.
	// No write happened.
	AddOutcomeElevationRequired
)

// AddResult is AddPlugin's full answer: which outcome fired, the parsed
// manifest (always populated on success, so a caller that needs to display
// permissions or route to elevation never re-parses), and the metadata
// record AddOutcomeInstalled wrote (zero value otherwise).
type AddResult struct {
	Outcome  AddOutcome
	Manifest plugin.Manifest
	Metadata PluginMetadata
}

// AddPlugin validates and (when no elevation is needed) installs a plugin.
//
//   - manifestSource is the raw manifest bytes (already read by the
//     caller — this package never touches a filesystem or a registry
//     fetch, both of which are CLI-ref-resolution concerns outside this
//     file's scope per the contract's own split between "validate the
//     plugin ref" (CLI) and "load and validate the manifest" (here)).
//   - checksum is the caller-supplied --checksum value, or "" when none
//     was given (VerifyArtifact is skipped entirely, not treated as a
//     vacuous pass, matching pkg/plugin.VerifyArtifact's own fail-closed
//     contract for when a check DOES run).
//   - artifact is the bundle bytes to verify checksum against; ignored
//     when checksum is "".
//   - daemonAvailable reports whether the caller has a live daemon
//     connection. When the manifest requires elevation (process-tier
//     runtime or a grant-expansion) and daemonAvailable is false, AddPlugin
//     refuses with ErrDaemonRequiredForElevatedPluginOp — the D/S-07.T4
//     daemonless elevated-verb refusal AC, enforced here (not only in
//     cmd/cascade) so it holds for every caller, present and future, not
//     just the one CLI command that exists today.
//
// Fail-closed: any parse, checksum, or elevation failure returns before
// store is ever touched.
func AddPlugin(ctx context.Context, store provider.Store, manifestSource []byte, checksum string, artifact []byte, daemonAvailable bool) (AddResult, error) {
	m, err := plugin.ParseManifest(bytes.NewReader(manifestSource))
	if err != nil {
		return AddResult{}, err
	}

	if checksum != "" {
		entry := plugin.RegistryVersionEntry{Checksum: checksum}
		if err := plugin.VerifyArtifact(entry, artifact); err != nil {
			return AddResult{}, err
		}
	}

	existing, ok, err := LoadMetadata(ctx, store, m.ID)
	if err != nil {
		return AddResult{}, err
	}
	if ok && existing.InstalledVersion == m.Version {
		return AddResult{Outcome: AddOutcomeAlreadyInstalled, Manifest: m, Metadata: existing}, nil
	}

	if addRequiresElevation(m, existing) {
		if !daemonAvailable {
			return AddResult{}, ErrDaemonRequiredForElevatedPluginOp("add")
		}
		return AddResult{Outcome: AddOutcomeElevationRequired, Manifest: m}, nil
	}

	rec := PluginMetadata{
		Name:             m.ID,
		InstalledVersion: m.Version,
		Enabled:          true,
		RuntimeMode:      m.Runtime,
		PinnedChecksum:   checksum,
		Grants:           append([]string(nil), m.Requires...),
	}
	if err := SaveMetadata(ctx, store, rec); err != nil {
		return AddResult{}, err
	}
	return AddResult{Outcome: AddOutcomeInstalled, Manifest: m, Metadata: rec}, nil
}

// addRequiresElevation is the §5.14 rule for `plugin add`: a process-tier
// runtime, OR a grant set that expands what an existing install (if any)
// already holds. A fresh install of a NON-process-tier plugin whose
// Requires is a subset of nothing (no prior install) is, by definition,
// requesting every capability it lists for the first time — which IS an
// expansion over the empty set, matching §5.14's "grant-expansion" wording
// literally (grantsExpand(nil, m.Requires) is true whenever Requires is
// non-empty). This is deliberate: a brand-new install that asks for any
// capability at all is exactly the case elevation exists to gate, and
// treating "no prior grants" as a free pass would let a first install of
// a maximally-permissioned plugin skip consent entirely.
func addRequiresElevation(m plugin.Manifest, existing PluginMetadata) bool {
	if m.Runtime == plugin.RuntimeProcess {
		return true
	}
	return grantsExpand(existing.Grants, m.Requires)
}

// AlreadyInstalledMessage renders the §5.9 idempotent-add message, kept
// here (not in cmd/cascade) so its wording has exactly one source.
func AlreadyInstalledMessage(name, version string) string {
	return "already installed at v" + version + " (" + name + ")"
}

// ErrDaemonRequiredForElevatedPluginOp is the D/S-07.T4 typed refusal a
// daemonless invocation of an elevated plugin verb returns. Exported so
// cmd/cascade/plugin.go and its tests assert against one literal rather
// than each formatting their own copy (the exact wording is itself part of
// the acceptance criterion).
func ErrDaemonRequiredForElevatedPluginOp(verb string) error {
	return cascade.Newf(cascade.KindUnavailable,
		"cascade plugin %s needs a running daemon: daemon required for elevated plugin operations "+
			"(process-tier runtime, grant expansion, and perms grant/revoke all need the daemon's "+
			"supervised plugin host). Start it with `cascade daemon start`.", verb)
}
