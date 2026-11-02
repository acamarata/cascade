// Package plugins (reverify.go): Purpose: re-establish trust in a process
// plugin whose executable changed after it was trusted (a cascade upgrade
// replaces the sibling while the manifest version stays), without a second
// trust source.
// Inputs: the plugin.update handler context (the elevation witness and the
// bound raw params), the signed registry index bytes and the installed
// version's manifest bytes carried in UpdateParams.
// Outputs: the record re-pinned to the sibling's current digest.
// Constraints: the witness names plugin.update and covers exactly these
// params; the record exists, was trusted before (carries an ExecDigest),
// is a process record held above the untrusted tier whose InstalledVersion is the manifest's version; the
// injected IndexVerifier accepts the index (the registry key); the index
// entry for this id and version carries a checksum equal to the params'
// checksum and to the sha256 of the manifest bytes; and the manifest's
// signed exec_sha256 for this platform equals the digest of the resolved
// sibling executable. Any failure is KindPolicyDenied naming the failed
// link and writes nothing; TrustGrants never change.
// SPORT: internal/plugins plugin reverify (P1-PLG-12).
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	goruntime "runtime"

	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// IndexVerifier verifies signed registry index bytes with the registry key
// and returns the index; the wire file injects it.
type IndexVerifier func(ctx context.Context, index []byte) (plugin.RegistryIndex, error)

// ReverifyCommand is the command that recovers id after its executable
// changed; LaunchInstalled's refusal and the docs quote it.
func ReverifyCommand(id string) string {
	return "cascade plugin update " + id + " --reverify"
}

// denyReverify refuses a reverify at the named failed link.
func denyReverify(id, link string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"plugin: %s: reverify refused (%s); trust is unchanged, and `cascade plugin remove %s` then "+
			"`cascade plugin add %s` remains the fallback", id, link, id, id)
}

// reverifyTrust re-pins p.ID's ExecDigest per this file's header.
func reverifyTrust(ctx context.Context, store provider.Store, verify IndexVerifier, p UpdateParams) (PluginMetadata, error) {
	m, err := witnessedReverify(ctx, p)
	if err != nil {
		return PluginMetadata{}, err
	}
	rec, err := reverifiableRecord(ctx, store, m)
	if err != nil {
		return PluginMetadata{}, err
	}
	if err := indexVouches(ctx, verify, p, m); err != nil {
		return PluginMetadata{}, err
	}
	digest, err := signedSiblingDigest(m)
	if err != nil {
		return PluginMetadata{}, err
	}
	rec.TrustTier, rec.ExecDigest = TrustTrusted, digest
	if err := SaveMetadata(ctx, store, rec); err != nil {
		return PluginMetadata{}, err
	}
	return rec, nil
}

// witnessedReverify checks the witness covers exactly p and returns the
// manifest p carries.
func witnessedReverify(ctx context.Context, p UpdateParams) (plugin.Manifest, error) {
	raw, err := witnessedInstallParams(ctx, UpdateRPCMethod, p.ID)
	if err != nil {
		return plugin.Manifest{}, err
	}
	var bound UpdateParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&bound); err != nil || !reflect.DeepEqual(bound, p) {
		return plugin.Manifest{}, denyTrust(p.ID, UpdateRPCMethod, "the witnessed params are not these reverify params")
	}
	if !p.Reverify {
		return plugin.Manifest{}, denyReverify(p.ID, "the call is not a reverify")
	}
	m, err := plugin.ParseManifest(bytes.NewReader(p.ManifestBytes))
	if err != nil {
		return plugin.Manifest{}, cascade.Wrapf(cascade.KindPolicyDenied, err, "plugin: %s: reverify refused (the manifest does not parse)", p.ID)
	}
	if m.ID != p.ID || m.Runtime != plugin.RuntimeProcess {
		return plugin.Manifest{}, denyReverify(p.ID, "the manifest is not this plugin's process manifest")
	}
	return m, nil
}

// reverifiableRecord loads the stored record and refuses unless it is a
// previously trusted process record at the manifest's version.
func reverifiableRecord(ctx context.Context, store provider.Store, m plugin.Manifest) (PluginMetadata, error) {
	rec, ok, err := LoadMetadata(ctx, store, m.ID)
	if err != nil {
		return PluginMetadata{}, err
	}
	switch {
	case !ok:
		return PluginMetadata{}, denyReverify(m.ID, "it is not installed")
	case rec.RuntimeMode != plugin.RuntimeProcess:
		return PluginMetadata{}, denyReverify(m.ID, "the installed record is not a process plugin")
	case rec.ExecDigest == "":
		return PluginMetadata{}, denyReverify(m.ID, "it was never trusted, so there is no prior executable digest to replace")
	case heldTier(rec) == process.TrustTierUntrusted:
		return PluginMetadata{}, denyReverify(m.ID, "its record is held at the untrusted tier; only a witnessed add restores trust")
	case rec.InstalledVersion != m.Version:
		return PluginMetadata{}, denyReverify(m.ID, "installed version "+rec.InstalledVersion+" is not the manifest's version "+m.Version)
	}
	return rec, nil
}

// heldTier is the process tier a record holds: untrusted unless it is
// marked trusted (the launch path alone assigns the trusted tier).
func heldTier(rec PluginMetadata) process.TrustTier {
	if rec.TrustTier == TrustTrusted {
		return ""
	}
	return process.TrustTierUntrusted
}

// indexVouches checks the index signature (the injected verifier) and that
// its entry for the manifest's id and version carries the checksum the
// params pin, which is the sha256 of the manifest bytes.
func indexVouches(ctx context.Context, verify IndexVerifier, p UpdateParams, m plugin.Manifest) error {
	if verify == nil || len(p.IndexBytes) == 0 {
		return denyReverify(p.ID, "no signed registry index to verify against")
	}
	idx, err := verify(ctx, p.IndexBytes)
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err, "plugin: %s: reverify refused (the registry index signature did not verify)", p.ID)
	}
	if idx.Sequence == 0 {
		return denyReverify(p.ID, "the registry index carries no sequence")
	}
	entry, ok := indexVersion(idx, m.ID, m.Version)
	if !ok {
		return denyReverify(p.ID, "the signed index has no entry for "+m.ID+" v"+m.Version)
	}
	if p.Checksum == "" || entry.Checksum != p.Checksum {
		return denyReverify(p.ID, "the signed index checksum is not the checksum the call pins")
	}
	if err := plugin.VerifyArtifact(plugin.RegistryVersionEntry{Checksum: entry.Checksum}, p.ManifestBytes); err != nil {
		return denyReverify(p.ID, "the signed index checksum is not the sha256 of the manifest bytes")
	}
	return nil
}

// indexVersion finds id at version in idx.
func indexVersion(idx plugin.RegistryIndex, id, version string) (plugin.RegistryVersionEntry, bool) {
	for _, e := range idx.Entries {
		if e.ID != id {
			continue
		}
		for _, v := range e.Versions {
			if v.Version == version {
				return v, true
			}
		}
	}
	return plugin.RegistryVersionEntry{}, false
}

// signedSiblingDigest returns the sibling executable's digest when it equals
// the manifest's signed exec_sha256 for this platform.
func signedSiblingDigest(m plugin.Manifest) (string, error) {
	signed, err := m.ExecDigestFor(goruntime.GOOS, goruntime.GOARCH)
	if err != nil {
		return "", denyReverify(m.ID, err.Error())
	}
	path, err := ResolveExecutable(m.ID)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindPolicyDenied, err, "plugin: %s: reverify refused (the executable is not trustable)", m.ID)
	}
	digest, err := ExecutableDigest(path)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindPolicyDenied, err, "plugin: %s: reverify refused (the executable cannot be hashed)", m.ID)
	}
	if digest != signed {
		return "", denyReverify(m.ID, "the signed executable digest does not match the installed executable")
	}
	return digest, nil
}
