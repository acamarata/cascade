// Purpose: the elevated `backup export` operation (05 §Epic S S-42.T2;
//
//	00-VISION principle 10): bundle one snapshot's portable content
//	(config/repo.json, the previous_snapshot chain's manifests, and the
//	target snapshot's own objects — S-41.T1's layout, S-41.T4's
//	object-scope precedent) into a single tar archive, then run it
//	through crypto.go's existing compress-then-encrypt stages (zstd, then
//	age to the repo's own recipient) unchanged. The tar's members stay
//	whatever they already were on Target: config/repo.json plaintext,
//	manifests/*.json and objects/*'s chunks already age-encrypted by
//	CreateSnapshot/Pipeline.Write. The outer wrap is a second, independent
//	layer over the whole bundle — this file introduces no new key or
//	signature scheme of its own (06 §5.1).
//
// Inputs: an ElevationProof, ExportOptions naming the Target and the
//
//	OPT-IN vault-export collaborator, and the SnapshotID to export.
//
// Outputs: one tar.zst.age byte slice, or a typed fail-closed error.
// Constraints: refuses (KindElevationRequired) without proof, exactly like
//
//	CreateSnapshot/Restore. The opt-in vault leg is OFF unless
//	IncludeVault is explicitly true AND a passphrase is supplied — the
//	zero-value ExportOptions can never emit vault material
//	(vaultexport.go's TestVaultExportOptInDefaultOff pins this).
//
// SPORT: internal.backup.export/ADDED (P1-E19-W4-S42-T2).

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrExportElevationRequired is ExportPortable's refusal for a missing
// proof — 06 §5.14's elevated-verb class (`backup export` ⚠).
var ErrExportElevationRequired = cascade.New(cascade.KindElevationRequired,
	"backup: export requires a valid elevation attestation")

// vaultBundleName is the tar member the OPT-IN §D-34 vault export rides
// under, when and only when ExportOptions.IncludeVault is true. Its
// presence or absence is the only signal ImportPortable uses to decide
// whether a restore-side vault import is attempted.
const vaultBundleName = "vault/export.age"

// ExportOptions collects ExportPortable's collaborators.
type ExportOptions struct {
	// Target is the repo Target ExportPortable reads S-41.T1's layout
	// from (config/, manifests/, objects/).
	Target Target
	// VaultBroker is the OPT-IN §D-34 collaborator: the Epic H vault
	// broker's elevated export verb. Nil is valid and is the default —
	// ExportPortable never dereferences it unless IncludeVault is true.
	VaultBroker VaultExporter
	// IncludeVault opts in to the passphrase-wrapped vault export.
	// The zero value (false) is the only default this package ever
	// assumes; nothing here infers opt-in from any other field.
	IncludeVault bool
	// VaultPassphrase is required when IncludeVault is true, and ignored
	// otherwise. Never persisted, never logged, never echoed in an error.
	VaultPassphrase string
}

// ExportPortable builds and returns one portable tar.zst.age artifact for
// id: config/repo.json, the previous_snapshot chain's manifests (so the
// restore side's integrity gate can chain-verify), and id's OWN manifest
// entries' objects only (S-41.T4's VerifyIntegrity object-scope
// precedent — an ancestor's objects that id's manifest does not itself
// reference are not bundled). With ExportOptions.IncludeVault set, the
// OPT-IN §D-34 passphrase-wrapped vault envelope rides alongside as one
// additional tar member, added BEFORE compress-then-encrypt so it too is
// covered by the outer wrap; it is never written or returned unwrapped.
func ExportPortable(ctx context.Context, proof ElevationProof, opts ExportOptions, id SnapshotID) ([]byte, error) {
	if proof == "" {
		return nil, ErrExportElevationRequired
	}
	if opts.Target == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: export requires a non-nil target")
	}
	if id == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "backup: export requires a snapshot id")
	}
	cfg, err := ReadRepoConfig(ctx, opts.Target)
	if err != nil {
		return nil, err
	}
	tarBytes, err := buildExportTar(ctx, opts, id)
	if err != nil {
		return nil, err
	}
	compressed, err := Compress(tarBytes)
	if err != nil {
		return nil, err
	}
	return Encrypt(cfg.AgeRecipient, compressed)
}

// buildExportTar assembles the uncompressed, unencrypted tar payload:
// config/repo.json, the id chain's manifests, id's own objects, and the
// opt-in vault member. Split out of ExportPortable to stay under the
// 50-line function cap.
func buildExportTar(ctx context.Context, opts ExportOptions, id SnapshotID) ([]byte, error) {
	identity, err := AgeIdentity()
	if err != nil {
		return nil, err
	}
	chainIDs, err := chaseChainIDs(ctx, opts.Target, identity, id)
	if err != nil {
		return nil, err
	}
	target, err := fetchManifestUnverified(ctx, opts.Target, identity, id)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := addTargetMember(ctx, w, opts.Target, repoConfigKey); err != nil {
		return nil, err
	}
	for _, cid := range chainIDs {
		if err := addTargetMember(ctx, w, opts.Target, manifestKey(cid)); err != nil {
			return nil, err
		}
	}
	if err := addManifestObjects(ctx, w, opts.Target, target); err != nil {
		return nil, err
	}
	if err := addVaultMember(ctx, w, opts); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: close export tar")
	}
	return buf.Bytes(), nil
}

// addManifestObjects adds every object id's OWN manifest entries
// reference — never an ancestor's — matching VerifyIntegrity's documented
// object-scope decision (integrity.go) exactly, so a portable export
// always carries precisely what that snapshot's own gate pass will read.
func addManifestObjects(ctx context.Context, w *tar.Writer, t Target, m Manifest) error {
	for _, entry := range m.Entries {
		for _, ref := range entry.Refs {
			objRef, err := manifestRefToObjectRef(ref)
			if err != nil {
				return err
			}
			if err := addTargetMember(ctx, w, t, ObjectKey(objRef.Hash)); err != nil {
				return err
			}
		}
	}
	return nil
}

// addTargetMember copies one raw, as-stored value from t at key into w
// under the same name — verbatim bytes, no transform, so an
// already-encrypted manifest or object rides through untouched.
func addTargetMember(ctx context.Context, w *tar.Writer, t Target, key string) error {
	rc, err := t.Get(ctx, key)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "backup: read %s for export", key)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "backup: read %s body for export", key)
	}
	return writeTarMember(w, key, data)
}

// writeTarMember writes one tar entry (name, data) with a fixed,
// deterministic header (mode/mtime never vary run-to-run for identical
// input — Art.7.3 determinism: two exports of the same snapshot with the
// same opt-in setting produce byte-identical tar payloads).
func writeTarMember(w *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o600,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
	}
	if err := w.WriteHeader(hdr); err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "backup: write tar header for %s", name)
	}
	if _, err := w.Write(data); err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "backup: write tar body for %s", name)
	}
	return nil
}
