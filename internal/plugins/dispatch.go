// Package plugins (dispatch.go): Purpose: the daemon-side composition root
// for an ELEVATED `cascade plugin add` install (D/S-07.T4's remaining
// gap, closed by this ticket's COMPLETION PASS): given a parsed manifest,
// decide whether this host can actually run its declared runtime,
// provision the plugin's isolated storage domain, and commit its
// metadata record.
//
// PACKAGE-BOUNDARY CORRECTION (recorded in this ticket's journal
// COMPLETION PASS section — this file's own existence corrects a claim
// repeated in lifecycle.go's doc comment and in P1-E15-W4-S31-T3/T4's own
// journals): those all state that ".golangci.yml's plugins-providers-
// boundary depguard rule denies ANY non-test file matching
// '**/plugins/**/*.go' from importing internal/**, which includes this
// file itself." That is false for a file at this exact path. Verified
// empirically (golangci-lint run ./internal/plugins/..., cache cleared,
// reproduced twice): a probe file placed directly at
// internal/plugins/<x>.go, importing internal/storage, produced ZERO
// depguard findings, while the identical import from
// internal/plugins/<subdir>/<x>.go was correctly flagged
// ("plugins-providers-boundary"). The rule's files glob
// ("**/plugins/**/*.go") only matches a file with at least one path
// segment BETWEEN "plugins/" and the file itself — internal/plugins/registry.go
// and cascadepa_wiring.go already rely on this exact same shape (they
// import internal/client, internal/memory, internal/runtime,
// internal/context, unflagged, today). This file follows that same,
// already-shipped precedent rather than inventing a new one.
// internal/plugins/process and internal/plugins/wasm (both subpackages,
// one directory further down) remain correctly bound to pkg/**-only, and
// this file imports them only as sibling plugins/** packages, never
// crossing that boundary itself.
//
// Inputs: a *sql.DB/migrate.Dialect/migrate.Clock/dbPath/backupDir tuple
// for the per-plugin migrator (mirrors internal/storage.NewPluginMigrator's
// own required argument list), the daemon's shared provider.Store, a
// *storage.PluginDomainRegistry the caller constructs ONCE and holds for
// the daemon process's lifetime (the registry has no persistence of its
// own — internal/storage.NewPluginDomainRegistry's own doc comment: "An
// empty registry"; re-creating it per call would make every domain claim
// look new every time), and the manifest plus checksum the CLI already
// parsed and verified.
//
// Outputs: on success, a PluginMetadata record committed to store. On a
// process-tier manifest, a typed refusal rather than a fabricated
// success: no code path in this tree marks a manifest's process.TrustTier
// above TrustTierUntrusted yet (there is no signing/allowlist mechanism),
// so an elevated add for a process-tier plugin can never actually launch
// — this composition root proves that with the real
// process.ProcessRuntime.Launch refusal instead of silently installing a
// metadata record for a runtime that can never start.
//
// SPORT: internal/plugins dispatch (ADD) — P1-E15-W4-S32-T4 COMPLETION PASS.
package plugins

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/plugins/wasm"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// pluginHostDomainOwner is the stable owner identity ProvisionElevated
// claims every installed plugin's storage domain under
// (storage.PluginDomainRegistry.Register's owner argument).
const pluginHostDomainOwner = "cascade.plugin-host"

// ProvisionElevated is the composition root the daemon's "plugin.add" RPC
// handler (internal/daemon/plugin_rpc.go) calls for an elevated install
// (process-tier, or a grant-expanding wasm/builtin manifest). domains
// must be the SAME *storage.PluginDomainRegistry instance across every
// call for the daemon's lifetime (see package doc).
func ProvisionElevated(
	ctx context.Context,
	db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string,
	store provider.Store, domains *storage.PluginDomainRegistry,
	m plugin.Manifest, checksum string,
) (PluginMetadata, error) {
	rec := PluginMetadata{
		Name:             m.ID,
		InstalledVersion: m.Version,
		Enabled:          true,
		RuntimeMode:      m.Runtime,
		PinnedChecksum:   checksum,
		Grants:           m.Requires,
	}

	switch m.Runtime {
	case plugin.RuntimeProcess:
		// No path in this tree ever sets a manifest's trust tier above
		// the default — every process-tier install evaluates through the
		// REAL process.ProcessRuntime.Launch trust gate here, which
		// always refuses TrustTierUntrusted before touching Stderr,
		// egress, or spawning anything (runtime.go's own first check).
		if _, err := process.NewProcessRuntime().Launch(ctx, process.Manifest{
			Name: m.ID, TrustTier: process.TrustTierUntrusted,
		}); err != nil {
			return PluginMetadata{}, cascade.Wrapf(cascade.KindPolicyDenied, err,
				"plugin: %s is a process-tier plugin and this host has no mechanism to mark any "+
					"plugin trusted yet", m.ID)
		}
		// Launch cannot succeed today (see above): the branch above
		// always returns. Left explicit, rather than folded into a
		// default case, so a future ticket that adds a real trust
		// mechanism finds this switch already shaped to fall through to
		// provisioning once Launch can succeed.
	case plugin.RuntimeWasm:
		limits := wasm.DefaultResourceLimits()
		rt, err := wasm.NewRuntime(ctx, limits.MemoryPages)
		if err != nil {
			return PluginMetadata{}, cascade.Wrapf(cascade.KindUnavailable, err,
				"plugin: %s: wasm runtime failed to initialize", m.ID)
		}
		defer func() { _ = rt.Close(ctx) }()
		rec.HostABIVersion = wasm.HostABIVersion()
	case plugin.RuntimeBuiltin:
		// Compiled into the host binary; no runtime construction needed.
	case plugin.RuntimeRemote:
		// P1-E15-W4-S33-T4 owns the remote runtime (handshake-only, P2
		// full execution) — not this ticket's files_scope.
		return PluginMetadata{}, cascade.Newf(cascade.KindUnsupported,
			"plugin: %s: remote-runtime plugins are not yet supported by elevated install", m.ID)
	default:
		return PluginMetadata{}, cascade.Newf(cascade.KindUnsupported,
			"plugin: %s: runtime %q has no elevated-install path yet", m.ID, m.Runtime)
	}

	if err := provisionStorageDomain(ctx, db, dialect, clock, dbPath, backupDir, store, domains, m.ID); err != nil {
		return PluginMetadata{}, err
	}

	if err := SaveMetadata(ctx, store, rec); err != nil {
		return PluginMetadata{}, err
	}
	return rec, nil
}

// provisionStorageDomain claims pluginID's storage domain (idempotent:
// re-claiming under the same owner is a no-op per
// PluginDomainRegistry.Register's own contract) and constructs its
// migrator-backed PluginStorage, so a later real plugin.Storage.Migrate
// call from the plugin's own runtime (once it declares real migrations)
// has a domain already registered and a real PluginStorage already able
// to serve it. PluginMigrator.Apply itself is deliberately NOT called
// here with an empty migration list: Apply's own validateMigrationVersions
// refuses zero migrations outright ("no migrations supplied") rather than
// treating it as a no-op, so there is nothing for an elevated install to
// apply yet — only the plugin's own manifest-declared migrations, applied
// when it actually runs, would ever populate that list.
func provisionStorageDomain(
	_ context.Context,
	db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string,
	store provider.Store, domains *storage.PluginDomainRegistry, pluginID string,
) error {
	if _, err := domains.Register(pluginID, pluginHostDomainOwner, 1); err != nil {
		return err
	}
	migrator, err := storage.NewPluginMigrator(db, dialect, clock, dbPath, backupDir, store)
	if err != nil {
		return err
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "plugin: build secret detector")
	}
	_, err = storage.NewPluginStorage(
		pluginID, store, metadataGrantChecker{store: store}, detectorSecretScanner{d: detector}, migrator)
	return err
}

// metadataGrantChecker is the real storage.GrantChecker: a plugin's
// cross-domain capability is whatever this ticket's own ChangePerms
// (lifecycle.go) already recorded in its PluginMetadata.Grants — the same
// grant set `cascade plugin perms grant/revoke` and `plugin info` already
// read and write, so this performs no parallel bookkeeping of its own.
type metadataGrantChecker struct {
	store provider.Store
}

// CheckGrant implements storage.GrantChecker.
func (g metadataGrantChecker) CheckGrant(ctx context.Context, subjectID, capability string) error {
	rec, ok, err := LoadMetadata(ctx, g.store, subjectID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindPermissionDenied, "plugin: %s is not installed, no grants to check", subjectID)
	}
	for _, granted := range rec.Grants {
		if granted == capability {
			return nil
		}
	}
	return cascade.Newf(cascade.KindPermissionDenied, "plugin: %s lacks capability %q", subjectID, capability)
}

// detectorSecretScanner adapts *secrets.Detector to storage.SecretScanner:
// ScanCertain is the "high-confidence credential-shaped span" leg
// SecretScanner's own doc comment specifies.
type detectorSecretScanner struct {
	d *secrets.Detector
}

// HasSecret implements storage.SecretScanner.
func (s detectorSecretScanner) HasSecret(content []byte) bool {
	return len(s.d.ScanCertain(content)) > 0
}
