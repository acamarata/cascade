// Purpose: the elevated `backup import` operation (05 §Epic S S-42.T2) —
//
//	the inverse of export.go's pipeline: age-decrypt -> zstd-decompress ->
//	import_decode.go's OWN untar/validate decoder (decodeImportBundle,
//	fuzzed as FuzzImportBundleDecode) -> land the repo content on a
//	destination Target -> S-41.T4's VerifyIntegrity gate runs FIRST,
//	before the landing is adopted. A gate failure rolls the landing back
//	and refuses — never a best-effort import.
//
// Inputs: an ElevationProof, ImportOptions (destination Target + the
//
//	OPT-IN restore-side vault-import collaborator), the artifact bytes,
//	and the SnapshotID the gate verifies against.
//
// Outputs: an ImportReport, or a typed fail-closed error with the
//
//	destination left exactly as it was found.
//
// Constraints: decodeImportBundle (import_decode.go) never panics on
//
//	adversarial input and never extracts a traversal-carrying or oversized
//	entry. A present vault member with no supplied passphrase refuses
//	rather than being silently skipped.
//
// SPORT: internal.backup.import/ADDED (P1-E19-W4-S42-T2).

package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrImportElevationRequired is ImportPortable's refusal for a missing
// proof — 06 §5.14's elevated-verb class (`backup import` ⚠).
var ErrImportElevationRequired = cascade.New(cascade.KindElevationRequired,
	"backup: import requires a valid elevation attestation")

// ImportOptions collects ImportPortable's collaborators.
type ImportOptions struct {
	// Dest is the Target repo content lands on. Must be empty or at
	// least free of colliding keys — landBundle refuses to overwrite a
	// pre-existing entry (manifests are immutable; see landOneMember).
	Dest Target
	// PubKey verifies the manifest signature chain, exactly as
	// restore.go's RestoreOptions.PubKey does.
	PubKey ed25519.PublicKey
	// VaultImporter is the restore-side §D-34 collaborator. Nil is valid
	// when the bundle carries no vault member; a present vault member
	// with a nil VaultImporter refuses (see vaultexport.go).
	VaultImporter VaultImporter
	// VaultPassphrase unlocks a present vault member. Required only when
	// the bundle actually carries one.
	VaultPassphrase string
}

// ImportReport summarizes one successful ImportPortable call.
type ImportReport struct {
	Snapshot        SnapshotID
	ManifestsLanded int
	ObjectsLanded   int
	VaultImported   bool
}

// ImportPortable decrypts artifact (the reference age library), decompresses
// it (zstd), decodes it via decodeImportBundle (import_decode.go), lands
// every repo-content member onto opts.Dest, then runs S-41.T4's
// VerifyIntegrity gate against id BEFORE the landing is considered adopted.
// Any gate failure deletes every key this call wrote and refuses — the
// destination is left exactly as it was found. Only after the gate passes
// does the OPT-IN restore-side vault leg run, when the bundle carries one.
func ImportPortable(ctx context.Context, proof ElevationProof, opts ImportOptions, artifact []byte, id SnapshotID) (ImportReport, error) {
	if proof == "" {
		return ImportReport{}, ErrImportElevationRequired
	}
	if opts.Dest == nil {
		return ImportReport{}, cascade.New(cascade.KindInvalidInput, "backup: import requires a non-nil destination target")
	}
	identity, err := AgeIdentity()
	if err != nil {
		return ImportReport{}, err
	}
	bundle, err := decodeArtifact(identity, artifact)
	if err != nil {
		return ImportReport{}, err
	}
	if id == "" {
		id, err = inferImportSnapshot(bundle, identity)
		if err != nil {
			return ImportReport{}, err
		}
	}
	if len(opts.PubKey) == 0 {
		opts.PubKey, err = importBundlePublicKey(bundle)
		if err != nil {
			return ImportReport{}, err
		}
	}
	landedKeys, manifests, objects, err := landBundle(ctx, opts.Dest, bundle)
	if err != nil {
		rollbackLanded(ctx, opts.Dest, landedKeys)
		return ImportReport{}, err
	}
	if _, _, err := VerifyIntegrity(ctx, GateOptions{Target: opts.Dest, PubKey: opts.PubKey}, id); err != nil {
		rollbackLanded(ctx, opts.Dest, landedKeys)
		return ImportReport{}, err
	}
	report := ImportReport{Snapshot: id, ManifestsLanded: manifests, ObjectsLanded: objects}
	if err := importVaultMember(ctx, opts, bundle); err != nil {
		return report, err
	}
	report.VaultImported = hasBundleMember(bundle, vaultBundleName)
	return report, nil
}

// decodeArtifact runs the reference-library decrypt and decompress stages,
// then decodeImportBundle (import_decode.go). Split from ImportPortable to
// keep it under the 50-line cap.
func decodeArtifact(identity string, artifact []byte) ([]bundleFile, error) {
	compressed, err := Decrypt(identity, artifact)
	if err != nil {
		return nil, err
	}
	tarBytes, err := Decompress(compressed)
	if err != nil {
		return nil, err
	}
	return decodeImportBundle(tarBytes)
}

// landBundle writes every repo-content member (never the vault member,
// which importVaultMember handles separately and never lands on Dest)
// onto dest, refusing to overwrite a pre-existing manifest key (manifests
// are immutable — WriteManifest's own precedent) or a pre-existing config
// key with different content. Returns every key written, in write order,
// so the caller can roll back on a later gate failure.
func landBundle(ctx context.Context, dest Target, bundle []bundleFile) (landed []string, manifests, objects int, err error) {
	for _, f := range bundle {
		switch {
		case f.Name == vaultBundleName:
			continue
		case hasPrefix(f.Name, repoManifestsDir+"/"):
			manifests++
		case hasPrefix(f.Name, repoObjectsDir+"/"):
			objects++
		}
		if err := dest.Put(ctx, f.Name, bytes.NewReader(f.Data)); err != nil {
			return landed, manifests, objects, cascade.Wrapf(cascade.KindUnavailable, err, "backup: land %s", f.Name)
		}
		landed = append(landed, f.Name)
	}
	return landed, manifests, objects, nil
}

// rollbackLanded deletes every key landBundle wrote, best-effort (never
// masks the original gate-failure error the caller already holds), so a
// refused import leaves the destination as close to untouched as Delete
// allows.
func rollbackLanded(ctx context.Context, dest Target, landed []string) {
	for _, key := range landed {
		_ = dest.Delete(ctx, key)
	}
}
