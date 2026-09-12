// Purpose: the OPT-IN §D-34 passphrase-wrapped vault export/import leg.
//
//	11-ROUND3-DELTAS.md §D-34 (verbatim): "`cascade backup export` gains
//	an OPT-IN passphrase-wrapped vault export (age-wrapped, same ceremony
//	pattern as S-42.T6), OFF by default; restore side imports it." The
//	contract names the collaborator as H/S-15.T1's
//	`Broker.Export(ctx, secrets.ExportRequest) ([]byte, error)` — as of
//	this ticket that method does not exist in the tree (S-15.T1 shipped
//	Get/Set/Rotate/List/Delete only; see the journal's contradiction
//	entry). VaultExporter/VaultImporter below are this ticket's own
//	narrow seam matching that intended shape exactly (ctx in, an opaque
//	passphrase-bearing request, already-wrapped bytes out — never a raw
//	secrets.Handle), so export.go/import.go depend on an interface, not a
//	concrete broker; a production Epic H adapter satisfying it is out of
//	this ticket's files_scope (internal/secrets is not in files_scope.add
//	or files_scope.change) and lands with S-42.T3's CLI/MCP wiring, the
//	same pattern S-42.T1's PutTarget/GetTarget used for symbols this
//	ticket's own BOUNDARY leaves temporarily uncalled by production code.
//
// Inputs: an ExportOptions/ImportOptions naming the broker collaborator
//
//	and, only when opting in, a non-empty passphrase.
//
// Outputs: an already-wrapped byte envelope (export) that this package
//
//	only ever moves, never decodes; nothing here (or the wrapping itself)
//	is this package's job — that lives entirely behind the interface.
//
// Constraints: OFF by default — IncludeVault's zero value is false, and no
//
//	code path here infers opt-in from the presence of a broker alone.
//	Plaintext secret material never reaches this file: VaultExporter
//	returns bytes already wrapped by its own real implementation, and this
//	file never unwraps them itself either.
//
// SPORT: internal.backup.vaultexport/ADDED (P1-E19-W4-S42-T2).

package backup

import (
	"archive/tar"
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// VaultExportRequest mirrors the passphrase-bearing request the (not yet
// landed) secrets.ExportRequest is specified to carry — this ticket's own
// copy exists only so export.go depends on a local, stable shape rather
// than importing a symbol that does not exist in the tree yet.
type VaultExportRequest struct {
	// Passphrase is the user-supplied §D-34 passphrase the real
	// implementation age-wraps the vault content with. Never persisted by
	// this package; held only for the duration of one call.
	Passphrase string
}

// VaultExporter is the elevated Epic H broker verb this leg calls. A real
// implementation returns bytes already passphrase-wrapped (the "same
// ceremony pattern as S-42.T6" the contract names) — this package never
// sees, holds, or infers a raw secret value from what it gets back.
type VaultExporter interface {
	Export(ctx context.Context, req VaultExportRequest) ([]byte, error)
}

// VaultImportRequest mirrors VaultExportRequest for the restore side.
type VaultImportRequest struct {
	// Wrapped is the exact envelope a prior VaultExporter.Export
	// produced — this package moves it verbatim, never decodes it.
	Wrapped []byte
	// Passphrase is required at import (§D-34: "required at import,
	// never persisted").
	Passphrase string
}

// VaultImporter is the restore-side counterpart: importing a wrapped
// envelope back into the vault through the real Epic H broker.
type VaultImporter interface {
	Import(ctx context.Context, req VaultImportRequest) error
}

// ErrVaultExportPassphraseRequired is the fail-closed refusal when
// IncludeVault is true but no passphrase was supplied — §D-34 "supplied
// at export"; opting in without a passphrase is a caller error, never a
// silent fallback to an unwrapped or empty envelope.
var ErrVaultExportPassphraseRequired = cascade.New(cascade.KindInvalidInput,
	"backup: opt-in vault export requires a passphrase")

// ErrVaultExportBrokerRequired is the fail-closed refusal when IncludeVault
// is true but no VaultExporter was supplied.
var ErrVaultExportBrokerRequired = cascade.New(cascade.KindInvalidInput,
	"backup: opt-in vault export requires a vault broker")

// ErrVaultImportPassphraseRequired is ImportPortable's refusal when a
// bundle carries a vault member but no passphrase was supplied to unlock
// it — the restore side never silently skips a present vault export.
var ErrVaultImportPassphraseRequired = cascade.New(cascade.KindInvalidInput,
	"backup: this bundle carries a vault export; a passphrase is required to import it")

// addVaultMember adds the OPT-IN vault export as one additional tar
// member, calling opts.VaultBroker exactly once. It is a pure no-op
// (returns nil, adds nothing) whenever IncludeVault is false — the
// structural guarantee that makes "off by default" provable rather than
// merely documented: no other field of ExportOptions can turn this on.
func addVaultMember(ctx context.Context, w *tar.Writer, opts ExportOptions) error {
	if !opts.IncludeVault {
		return nil
	}
	if opts.VaultPassphrase == "" {
		return ErrVaultExportPassphraseRequired
	}
	if opts.VaultBroker == nil {
		return ErrVaultExportBrokerRequired
	}
	wrapped, err := opts.VaultBroker.Export(ctx, VaultExportRequest{Passphrase: opts.VaultPassphrase})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: opt-in vault export")
	}
	return writeTarMember(w, vaultBundleName, wrapped)
}

// importVaultMember runs the restore-side §D-34 leg when bundle carries a
// vault member: it requires both a passphrase and a VaultImporter,
// refusing rather than silently skipping a present vault export (the leg
// S-42.T5's drill proves against the opted-out re-auth-runbook
// alternative — a present-but-unimported vault export would falsify that
// drill either way it is misread).
func importVaultMember(ctx context.Context, opts ImportOptions, bundle []bundleFile) error {
	wrapped, present := findBundleMember(bundle, vaultBundleName)
	if !present {
		return nil
	}
	if opts.VaultPassphrase == "" {
		return ErrVaultImportPassphraseRequired
	}
	if opts.VaultImporter == nil {
		return cascade.New(cascade.KindInvalidInput,
			"backup: this bundle carries a vault export; a vault importer is required to import it")
	}
	return opts.VaultImporter.Import(ctx, VaultImportRequest{Wrapped: wrapped, Passphrase: opts.VaultPassphrase})
}

// findBundleMember returns the named member's data, if present.
func findBundleMember(bundle []bundleFile, name string) ([]byte, bool) {
	for _, f := range bundle {
		if f.Name == name {
			return f.Data, true
		}
	}
	return nil, false
}

// hasBundleMember reports whether bundle carries a member named name.
func hasBundleMember(bundle []bundleFile, name string) bool {
	_, ok := findBundleMember(bundle, name)
	return ok
}
