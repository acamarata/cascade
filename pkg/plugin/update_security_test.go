// Purpose: the S-50.T8 REWORK pass's own new tests — adversarial CR
//
//	(/tmp/cascade-evidence/s50t8-cr-verdict.txt) FIX-4 (signature
//	verification, both the "empty" and the "checksum matches but signature
//	does not" shapes) and FIX-8 (pre-release/downgrade comparison
//	refusals) — split into a THIRD file purely to keep update_test.go and
//	update_e2e_test.go under Art.10.3's 300-line cap while landing every
//	named check; same established precedent update_e2e_test.go's own
//	header records (registry_client_test.go/registry_tamper_test.go/
//	registry_example_test.go/registry_fuzz_test.go are four files for one
//	ticket's "the" test file too). SCOPE DEVIATION from the ticket's
//	files_scope (pkg/plugin/update_test.go only), recorded per LANE-RULES
//	§1 for the identical reason update_e2e_test.go already records one.
//
// SPORT: pkg/plugin registry-client-update tests (CHANGE, rework) —
//
//	P1-E24-W5-S50-T8.
package plugin_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// checksumOf hex-encodes the SHA-256 digest of s, matching what a real
// registry entry's Checksum field carries.
func checksumOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// passthroughVerifier is a plugin.RegistryVerifier double that parses an
// index document without any cryptographic check. It exists ONLY for the
// pre-release/downgrade comparison tests below, which need an arbitrary
// custom version-ordering INPUT and are not exercising the signature-
// verification layer itself — that layer has its own dedicated real-key
// tests in registry_test.go, verify_artifact_test.go, and this file's own
// signature-refusal tests (which use the real grantDiffVerifier key, not
// this double).
type passthroughVerifier struct{}

func (passthroughVerifier) VerifyIndex(_ context.Context, data []byte) (plugin.RegistryIndex, error) {
	var idx plugin.RegistryIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return plugin.RegistryIndex{}, err
	}
	return idx, nil
}

func (passthroughVerifier) VerifyArtifact(context.Context, []byte, plugin.RegistryVersionEntry) error {
	return nil
}

// passthroughClient builds an index containing exactly one entry/version
// and a client that trusts it unconditionally (passthroughVerifier).
func passthroughClient(t *testing.T, latestVersion string) *plugin.RegistryClient {
	t.Helper()
	idx := plugin.RegistryIndex{
		SchemaVersion: "1",
		Entries: []plugin.RegistryIndexEntry{{
			ID: "semver-demo", LatestVersion: latestVersion,
			Versions: []plugin.RegistryVersionEntry{{Version: latestVersion, Checksum: "ab"}},
		}},
	}
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal fixture index: %v", err)
	}
	return plugin.NewRegistryClient(plugin.RegistryConfig{}, updateFakeFetcher{index: data, artifactOK: true},
		passthroughVerifier{}, nil, &fakeClock{now: time.Unix(2000, 0)})
}

// TestCheckUpdate_RefusesPrereleaseInstalledVersion is FIX-8: the
// INSTALLED version carries a pre-release suffix, so ordering it against
// the registry's plain latest_version must refuse rather than guess.
func TestCheckUpdate_RefusesPrereleaseInstalledVersion(t *testing.T) {
	client := passthroughClient(t, "1.2.0")
	_, err := client.CheckUpdate(context.Background(), "semver-demo", "1.2.0-rc1")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("CheckUpdate(installed=1.2.0-rc1, latest=1.2.0) err = %v, want KindInvalidInput", err)
	}
	if got := err.Error(); !strings.Contains(got, "1.2.0-rc1") || !strings.Contains(got, "1.2.0") {
		t.Fatalf("err = %q, want it to name both versions", got)
	}
}

// TestCheckUpdate_RefusesPrereleaseCandidateVersion is FIX-8's other side:
// the REGISTRY's own latest_version is itself a pre-release.
func TestCheckUpdate_RefusesPrereleaseCandidateVersion(t *testing.T) {
	client := passthroughClient(t, "1.3.0-beta1")
	_, err := client.CheckUpdate(context.Background(), "semver-demo", "1.2.0")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("CheckUpdate(installed=1.2.0, latest=1.3.0-beta1) err = %v, want KindInvalidInput", err)
	}
}

// TestCheckUpdate_StrictDowngradeRefused is a strict (not equal-version)
// downgrade: the registry's latest_version is OLDER than what is
// installed. CheckUpdate must report "no update available" (nil, nil),
// never an update and never an error.
func TestCheckUpdate_StrictDowngradeRefused(t *testing.T) {
	client := passthroughClient(t, "0.9.0")
	entry, err := client.CheckUpdate(context.Background(), "semver-demo", "1.2.0")
	if err != nil {
		t.Fatalf("CheckUpdate(installed=1.2.0, latest=0.9.0): %v", err)
	}
	if entry != nil {
		t.Fatalf("CheckUpdate(installed newer than registry latest) = %+v, want nil (no downgrade offered)", entry)
	}
}

// TestStageAndVerifyArtifact_EmptySignatureRefused is FIX-4 exercised at
// the StageAndVerifyArtifact seam runPluginUpdateFromRegistry actually
// calls: an entry whose checksum matches the artifact bytes but whose
// Signature is empty must still be refused (registry_verify.go's own
// fixed KindPolicyDenied), and the staged file cleaned up regardless.
func TestStageAndVerifyArtifact_EmptySignatureRefused(t *testing.T) {
	artifacts := map[string][]byte{
		"https://registry.example.com/grant-diff-demo/1.1.0.plugin": []byte(grantDiffCandidateManifestTOML),
	}
	// grantDiffFixtureBytes' own 1.1.0 entry carries a real signature
	// (README.md); this test needs an otherwise-identical entry with the
	// signature stripped, so it builds one directly rather than mutating
	// the committed fixture.
	entry := plugin.RegistryVersionEntry{
		Version:     "1.1.0",
		DownloadURL: "https://registry.example.com/grant-diff-demo/1.1.0.plugin",
		Checksum:    checksumOf(grantDiffCandidateManifestTOML),
		Signature:   "",
	}
	client := grantDiffClient(t, updateFakeFetcher{artifactOK: true, artifacts: artifacts})
	dir := t.TempDir()
	_, err := client.StageAndVerifyArtifact(context.Background(), entry, dir)
	if err == nil {
		t.Fatal("StageAndVerifyArtifact(matching checksum, empty signature) = nil error, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("StageAndVerifyArtifact(empty signature) err = %v, want KindPolicyDenied", err)
	}
	assertStagingEmpty(t, dir)
}

// TestStageAndVerifyArtifact_NonVerifyingSignatureRefused is FIX-4's other
// craft (the CR verdict's own example): a checksum that matches, paired
// with a well-formed but WRONG Ed25519 signature (64 zero bytes, valid
// length, never a real signature over these bytes). The checksum-only
// free function VerifyArtifact would accept this; the real
// c.verifier.VerifyArtifact this method now calls must not.
func TestStageAndVerifyArtifact_NonVerifyingSignatureRefused(t *testing.T) {
	artifacts := map[string][]byte{
		"https://registry.example.com/grant-diff-demo/1.1.0.plugin": []byte(grantDiffCandidateManifestTOML),
	}
	entry := plugin.RegistryVersionEntry{
		Version:     "1.1.0",
		DownloadURL: "https://registry.example.com/grant-diff-demo/1.1.0.plugin",
		Checksum:    checksumOf(grantDiffCandidateManifestTOML),
		Signature:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==", // 64 zero bytes, base64 (ed25519.SignatureSize)
	}
	client := grantDiffClient(t, updateFakeFetcher{artifactOK: true, artifacts: artifacts})
	dir := t.TempDir()
	_, err := client.StageAndVerifyArtifact(context.Background(), entry, dir)
	if err == nil {
		t.Fatal("StageAndVerifyArtifact(matching checksum, non-verifying signature) = nil error, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("StageAndVerifyArtifact(non-verifying signature) err = %v, want KindIntegrity", err)
	}
	assertStagingEmpty(t, dir)
}
