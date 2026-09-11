// Package generator is the first-party registry index publisher: it scans
// a directory of plugin artifacts plus their manifests and produces the
// signed pkg/plugin.RegistryIndex document X/S-50.T1's client fetches.
//
// Purpose: publisher-side tooling — GenerateIndex builds the index,
//
//	SignIndex signs it, WriteIndex persists it atomically.
//
// Inputs: a GeneratorConfig naming an artifacts directory and base URL for
//
//	GenerateIndex; a built *plugin.RegistryIndex plus a vault key-ref for
//	SignIndex; a signed *plugin.RegistryIndex plus an output directory for
//	WriteIndex.
//
// Outputs: a deterministic *plugin.RegistryIndex; a detached-in-envelope
//
//	Ed25519 signature (base64); an atomically written index.json.
//
// Constraints: pkg/ may not import internal/ (Art.10.2); no bare
//
//	fmt.Errorf/errors.New — every error is a *cascade.Error; no bare
//	time.Now (Art.7 §3 — this package reads no wall clock at all).
//
// CONTRACT-VS-TREE CONTRADICTIONS (documented at length in the ticket
// journal; summarized here since the code embodies the resolution):
//
//  1. The ticket describes a detached minisign v2 signature
//     (index.json.minisig) signed with a "minisign private key" retrieved
//     from vault. X/S-50.T1's real client (pkg/plugin/registry_verify.go)
//     does not implement minisign: RegistryFetcher.FetchIndex returns one
//     []byte and RegistryIndex.Signature is a field embedded in that same
//     JSON document, structurally unable to carry a second minisig file
//     (see pkg/plugin/testdata/registry/README.md, filed by T1). SignIndex
//     therefore signs with the same pure-Go Ed25519 primitive the real
//     verifier checks — the only signature this client can ever accept —
//     and WriteIndex writes exactly one file, index.json. No
//     index.json.minisig is produced; producing one would be a file the
//     real client can never consume.
//  2. The ticket's Entry type is flat (Version, RuntimeMode,
//     ChecksumSHA256, PluginURL, ManifestURL directly on one record). The
//     real schema (pkg/plugin/registry.go) is RegistryIndex{Entries
//     []RegistryIndexEntry{ID, Name, Description, Tags, LatestVersion,
//     Versions []RegistryVersionEntry{Version, HostVersion, DownloadURL,
//     Checksum, Signature}}} — one entry per plugin ID, multiple versions
//     nested. GenerateIndex groups artifacts by manifest ID into that
//     shape. RuntimeMode and ManifestURL have no home in the real schema
//     and are dropped; ChecksumSHA256 is the real Checksum field.
//  3. The ticket assumes ".manifest.json" siblings, but the manifest v2
//     schema (pkg/plugin/loader.go) is TOML-only by binding ruling R-14.10
//     ("no format parameter, no second wire-format decoder"). This package
//     reads ".manifest.toml" siblings via plugin.ParseManifest.
//  4. Manifest (pkg/plugin/manifest.go) carries no Description or Tags
//     field at all — only ID, Name, Schema, Version, HostVersion, Runtime,
//     Provides, Requires, Permissions. RegistryIndexEntry.Description and
//     .Tags therefore cannot be populated from a manifest as the ticket
//     assumed; GenerateIndex leaves them empty. This is a real, disclosed
//     gap: the manifest v2 schema has no field this generator could read
//     them from, and adding one is a manifest-schema change outside this
//     ticket's files_scope.
//
// SPORT: pkg/plugin/registry/generator (ADD) — P1-E24-W5-S50-T5.
package generator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// artifactExt and manifestExt name the two co-located files GenerateIndex
// pairs up for each published plugin version.
const (
	artifactExt = ".plugin"
	manifestExt = ".manifest.toml"
)

var (
	// ErrMissingManifest is returned when an artifact file has no
	// co-located manifest sibling.
	ErrMissingManifest = &cascade.Error{Kind: cascade.KindInvalidInput, Msg: "registry-gen: artifact has no co-located manifest"}
	// ErrInvalidManifest is returned when a manifest sibling fails
	// cascade.plugin/v2 schema validation (plugin.ParseManifest).
	ErrInvalidManifest = &cascade.Error{Kind: cascade.KindInvalidInput, Msg: "registry-gen: manifest failed v2 schema validation"}
)

// VaultClient retrieves signing key material by an opaque reference. The
// real implementation (internal/tools/registry-gen's composition root)
// wraps internal/secrets.Broker.Get; this package declares only the seam,
// per pkg/'s internal/-import boundary.
type VaultClient interface {
	// Get returns the raw key bytes named by keyRef, or an error if the
	// reference cannot be resolved.
	Get(ctx context.Context, keyRef string) ([]byte, error)
}

// GeneratorConfig configures GenerateIndex.
//
//nolint:revive // name fixed by the ticket contract (05-PEWS-PLAN §S-50.T5); renaming would break the documented public surface
type GeneratorConfig struct {
	// ArtifactsDir is the directory containing <name>-<version>.plugin
	// artifact files paired with <name>-<version>.manifest.toml manifests.
	ArtifactsDir string
	// BaseURL prefixes every generated Entry's DownloadURL.
	BaseURL string
}

// GenerateIndex scans cfg.ArtifactsDir for artifact/manifest pairs and
// returns a deterministic *plugin.RegistryIndex: entries sorted by plugin
// ID, each entry's versions sorted by version string, LatestVersion set to
// the lexicographically-last version (a semver-major-aware ordering is not
// implemented; documented in the journal as a known simplification for an
// S-weight ticket). GenerateIndex reads no wall clock and carries no
// timestamp field, so two calls over the same inputs are byte-identical.
func GenerateIndex(cfg GeneratorConfig) (*plugin.RegistryIndex, error) {
	bases, err := discoverArtifacts(cfg.ArtifactsDir)
	if err != nil {
		return nil, err
	}
	byID := map[string]*plugin.RegistryIndexEntry{}
	var order []string
	for _, base := range bases {
		id, name, ve, err := buildVersionEntry(cfg, base)
		if err != nil {
			return nil, err
		}
		e, ok := byID[id]
		if !ok {
			e = &plugin.RegistryIndexEntry{ID: id, Name: name}
			byID[id] = e
			order = append(order, id)
		}
		e.Versions = append(e.Versions, ve)
	}
	sort.Strings(order)
	entries := make([]plugin.RegistryIndexEntry, 0, len(order))
	for _, id := range order {
		e := byID[id]
		sort.Slice(e.Versions, func(i, j int) bool { return e.Versions[i].Version < e.Versions[j].Version })
		e.LatestVersion = e.Versions[len(e.Versions)-1].Version
		entries = append(entries, *e)
	}
	return &plugin.RegistryIndex{SchemaVersion: plugin.SchemaVersionCurrent, Entries: entries}, nil
}

// discoverArtifacts returns the sorted, extension-stripped base names of
// every *.plugin file directly under dir (no recursion — a flat release
// directory, per the ticket's own input description).
func discoverArtifacts(dir string) ([]string, error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "registry-gen: read artifacts dir %q", dir)
	}
	var bases []string
	for _, de := range dirents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), artifactExt) {
			continue
		}
		bases = append(bases, strings.TrimSuffix(de.Name(), artifactExt))
	}
	sort.Strings(bases)
	return bases, nil
}

// buildVersionEntry pairs one artifact with its manifest sibling, computes
// the artifact's SHA-256 checksum, and returns the manifest's plugin ID and
// name plus the RegistryVersionEntry the pair contributes.
func buildVersionEntry(cfg GeneratorConfig, base string) (id, name string, ve plugin.RegistryVersionEntry, err error) {
	manifestPath := filepath.Join(cfg.ArtifactsDir, base+manifestExt)
	mf, openErr := os.Open(manifestPath)
	if openErr != nil {
		return "", "", ve, cascade.Wrapf(ErrMissingManifest.Kind, openErr, "%s: %s", ErrMissingManifest.Msg, base)
	}
	defer func() { _ = mf.Close() }()

	m, parseErr := plugin.ParseManifest(mf)
	if parseErr != nil {
		return "", "", ve, cascade.Wrapf(ErrInvalidManifest.Kind, parseErr, "%s: %s", ErrInvalidManifest.Msg, base)
	}

	artifactPath := filepath.Join(cfg.ArtifactsDir, base+artifactExt)
	data, readErr := os.ReadFile(artifactPath)
	if readErr != nil {
		return "", "", ve, cascade.Wrapf(cascade.KindInvalidInput, readErr, "registry-gen: read artifact %q", artifactPath)
	}
	sum := sha256.Sum256(data)

	ve = plugin.RegistryVersionEntry{
		Version:     m.Version,
		HostVersion: m.HostVersion,
		DownloadURL: strings.TrimRight(cfg.BaseURL, "/") + "/" + base + artifactExt,
		Checksum:    hex.EncodeToString(sum[:]),
	}
	return m.ID, m.Name, ve, nil
}

// signedPayload mirrors pkg/plugin's unexported signedPayload exactly
// (field names, order, and json tags): schema_version plus the raw
// entries bytes, excluding Signature. Marshaling the same *plugin.Index's
// Entries slice through this struct and through the final RegistryIndex
// envelope produces byte-identical "entries" bytes in both places, so the
// signature SignIndex computes here is exactly what
// Ed25519Verifier.VerifyIndex reconstructs and checks.
type signedPayload struct {
	SchemaVersion string          `json:"schema_version"`
	Entries       json.RawMessage `json:"entries"`
}

// SignIndex signs idx with the Ed25519 private key named by keyRef and
// sets idx.Signature to the base64-encoded (standard encoding) detached
// signature. keyRef is never accepted as inline key material — only an
// opaque reference resolved through vault.
func SignIndex(idx *plugin.RegistryIndex, keyRef string, vault VaultClient) error {
	priv, err := vault.Get(context.Background(), keyRef)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry-gen: retrieve signing key %q", keyRef)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return cascade.Newf(cascade.KindInvalidInput, "registry-gen: signing key %q is %d bytes, want %d", keyRef, len(priv), ed25519.PrivateKeySize)
	}
	entriesRaw, err := json.Marshal(idx.Entries)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "registry-gen: marshal entries")
	}
	payload, err := json.Marshal(signedPayload{SchemaVersion: idx.SchemaVersion, Entries: entriesRaw})
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "registry-gen: marshal signed payload")
	}
	sig := ed25519.Sign(ed25519.PrivateKey(priv), payload)
	idx.Signature = base64.StdEncoding.EncodeToString(sig)
	return nil
}

// WriteIndex marshals the signed idx to compact JSON (matching the byte
// shape Ed25519Verifier.VerifyIndex re-derives — no indentation, HTML
// escaping left at encoding/json's default so ">=" host-version ranges
// encode identically to the T1 fixture) and writes it to
// <outDir>/index.json atomically: a temp file in outDir, then renamed over
// the final path, so a reader never observes a partial write.
func WriteIndex(idx *plugin.RegistryIndex, outDir string) error {
	data, err := json.Marshal(idx)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "registry-gen: marshal index")
	}
	final := filepath.Join(outDir, "index.json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry-gen: write temp index")
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry-gen: rename index into place")
	}
	return nil
}
