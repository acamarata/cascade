// Purpose: infer the exported chain tip when CLI import has no snapshot-id shape.
// Inputs: decoded bundle members and the backup age identity.
// Outputs: the unique manifest not referenced as another manifest's predecessor.
// Constraints: malformed, mismatched, duplicate, or ambiguous chains fail closed.
// SPORT: internal.backup.import/CHANGE (P1-E19-W4-S42-T3).

package backup

import (
	"crypto/ed25519"
	"encoding/base64"

	"github.com/acamarata/cascade/pkg/cascade"
)

func inferImportSnapshot(bundle []bundleFile, identity string) (SnapshotID, error) {
	manifests := make(map[SnapshotID]Manifest)
	referenced := make(map[SnapshotID]bool)
	for _, file := range bundle {
		id, ok := snapshotIDFromManifestKey(file.Name)
		if !ok {
			continue
		}
		plain, err := Decrypt(identity, file.Data)
		if err != nil {
			return "", err
		}
		manifest, err := DecodeManifestUnverified(plain)
		if err != nil {
			return "", err
		}
		if manifest.Snapshot != id || manifests[id].Snapshot != "" {
			return "", cascade.New(cascade.KindIntegrity,
				"backup: import bundle has a duplicate or mismatched manifest")
		}
		manifests[id] = manifest
		if manifest.PreviousSnapshot != "" {
			referenced[manifest.PreviousSnapshot] = true
		}
	}
	var tip SnapshotID
	for id := range manifests {
		if referenced[id] {
			continue
		}
		if tip != "" {
			return "", cascade.New(cascade.KindIntegrity, "backup: import bundle has multiple chain tips")
		}
		tip = id
	}
	if tip == "" {
		return "", cascade.New(cascade.KindIntegrity, "backup: import bundle has no manifest chain tip")
	}
	return tip, nil
}

func importBundlePublicKey(bundle []bundleFile) (ed25519.PublicKey, error) {
	data, ok := findBundleMember(bundle, repoConfigKey)
	if !ok {
		return nil, cascade.New(cascade.KindIntegrity, "backup: import bundle has no repository config")
	}
	config, err := DecodeRepoConfig(data)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(config.ManifestSigningPubKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindIntegrity,
			"backup: import bundle repository signing key is invalid")
	}
	return ed25519.PublicKey(key), nil
}
