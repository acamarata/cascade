// Purpose: X/S-50.T6 — systematic negative-path coverage of RegistryClient
//
//	and VerifyArtifact against tampered registry data, each case backed by
//	a fixture in testdata/tamper/ derived from the real T1 fixture
//	(testdata/tamper/README.md documents provenance, Art.2 §2). This file
//	authors no new production symbol: VerifyArtifact and ErrChecksumMismatch
//	are X/S-50.T1's (verify_artifact.go, registry_errors.go).
//
// Inputs: testdata/tamper/*.json.
// Outputs: none — pass/fail only.
// Constraints: zero network calls; all cache IO under t.TempDir(); -race.
//
// CONTRACT-VS-TREE NOTE: the ticket's files_scope names
// pkg/plugin/registry/client_tamper_test.go and
// pkg/plugin/registry/testdata/tamper/*.minisig. Neither pkg/plugin/registry/
// nor any *.minisig file exists or can exist against the real client (see
// registry_client.go's own doc comment and testdata/registry/README.md,
// both filed by X/S-50.T1): the real registry-client code lives flat in
// pkg/plugin/, and the signature is one field of the single JSON document
// fetched, never a detached sibling file. This file lands at
// pkg/plugin/registry_tamper_test.go, matching every other X/S-50.T1 test
// file's location, and every fixture is *.json (see testdata/tamper/README.md
// for the extension rationale).
//
// SPORT: pkg/plugin registry-client tamper tests (ADD) — P1-E24-W5-S50-T6.
package plugin_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// ExampleVerifyArtifact demonstrates the checksum-boundary contract this
// file's TestTamperChecksumMismatch exercises: a verified index says
// nothing about any one artifact's bytes until VerifyArtifact checks them
// too (Art.10 §4/§6 — no runnable Example existed for this exported
// symbol before this ticket).
func ExampleVerifyArtifact() {
	artifact := []byte("plugin artifact bytes")
	sum := sha256.Sum256(artifact)
	entry := plugin.RegistryVersionEntry{Checksum: hex.EncodeToString(sum[:])}

	if err := plugin.VerifyArtifact(entry, artifact); err != nil {
		fmt.Println("unexpected:", err)
	} else {
		fmt.Println("artifact matches its published checksum")
	}

	if err := plugin.VerifyArtifact(entry, []byte("different bytes")); err == nil {
		fmt.Println("unexpected: tampered artifact was accepted")
	} else {
		fmt.Println("tampered artifact rejected")
	}
	// Output:
	// artifact matches its published checksum
	// tampered artifact rejected
}

func tamperFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "tamper", name))
	if err != nil {
		t.Fatalf("read tamper fixture %s: %v", name, err)
	}
	return data
}

// altPublicKey is checksum-mismatch.json's own signing key's public half
// (see testdata/tamper/README.md); it verifies ONLY that fixture, never
// the real T1 fixture.
func altPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString("MrU+iC49qsGA16X2Ik1htkAbQ/kB2wnw5NhM5OSicYs=")
	if err != nil {
		t.Fatalf("decode alt public key: %v", err)
	}
	return ed25519.PublicKey(raw)
}

// tamperClient builds a RegistryClient over a real FileCache rooted at
// t.TempDir(), so cache-file-absence assertions inspect a real filesystem.
func tamperClient(t *testing.T, data []byte, verifier plugin.RegistryVerifier) (*plugin.RegistryClient, string) {
	t.Helper()
	dir := t.TempDir()
	cache := plugin.FileCache{Dir: dir}
	client := plugin.NewRegistryClient(
		plugin.RegistryConfig{CacheTTL: time.Hour},
		&fakeFetcher{index: data},
		verifier,
		cache,
		&fakeClock{now: time.Unix(1000, 0)},
	)
	return client, dir
}

func assertNoCachedIndex(t *testing.T, cacheDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cacheDir, "index.json")); !os.IsNotExist(err) {
		t.Fatalf("cache dir %s contains index.json after a failed Fetch (err=%v); payload must never be cached on verification failure", cacheDir, err)
	}
}

// TestTamperChecksumMismatch: the index-vs-artifact verification boundary.
// Fetch validates the SIGNATURE over the whole index and succeeds; a
// per-entry checksum is a SEPARATE property VerifyArtifact alone checks —
// Fetch has no artifact bytes to check it against. A caller that trusts a
// verified index without also calling VerifyArtifact before using a
// downloaded artifact skips a real check this boundary does not perform
// for them.
func TestTamperChecksumMismatch(t *testing.T) {
	data := tamperFixture(t, "checksum-mismatch.json")
	verifier := plugin.Ed25519Verifier{PublicKey: altPublicKey(t)}
	client, _ := tamperClient(t, data, verifier)

	idx, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch(checksum-mismatch) = %v, want a verified *Index (index signature is valid)", err)
	}
	if len(idx.Entries) != 1 || len(idx.Entries[0].Versions) != 1 {
		t.Fatalf("unexpected fixture shape: %+v", idx)
	}
	entry := idx.Entries[0].Versions[0]

	err = plugin.VerifyArtifact(entry, []byte("these are not the artifact bytes the checksum names"))
	if err == nil {
		t.Fatal("VerifyArtifact on the tampered entry returned nil, want ErrChecksumMismatch")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact err kind = %v, want KindIntegrity (ErrChecksumMismatch)", err)
	}
}

func TestTamperSignatureTruncated(t *testing.T) {
	data := tamperFixture(t, "sig-truncated.json")
	client, dir := tamperClient(t, data, realVerifier(t))

	_, err := client.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Fetch(sig-truncated) err = %v, want KindIntegrity (ErrSignatureInvalid)", err)
	}
	assertNoCachedIndex(t, dir)
}

func TestTamperSignatureWrongKeyBody(t *testing.T) {
	data := tamperFixture(t, "sig-wrong-key-body.json")
	client, dir := tamperClient(t, data, realVerifier(t))

	_, err := client.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Fetch(sig-wrong-key-body) err = %v, want KindIntegrity (ErrSignatureInvalid)", err)
	}
	assertNoCachedIndex(t, dir)
}

func TestTamperBodyModifiedAfterSign(t *testing.T) {
	data := tamperFixture(t, "body-modified-after-sign.json")
	client, dir := tamperClient(t, data, realVerifier(t))

	_, err := client.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Fetch(body-modified-after-sign) err = %v, want KindIntegrity (ErrSignatureInvalid)", err)
	}
	assertNoCachedIndex(t, dir)
}

func TestTamperIndexMalformedJSON(t *testing.T) {
	data := tamperFixture(t, "malformed.json")
	client, dir := tamperClient(t, data, realVerifier(t))

	_, err := client.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Fetch(malformed) err = %v, want KindInvalidInput (ErrIndexMalformed)", err)
	}
	assertNoCachedIndex(t, dir)
}

func TestTamperEmptySignatureFile(t *testing.T) {
	data := tamperFixture(t, "empty-signature.json")
	client, dir := tamperClient(t, data, realVerifier(t))

	_, err := client.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch(empty-signature) returned nil error, want non-nil (empty signature must never verify)")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Fetch(empty-signature) err = %v, want KindIntegrity (ErrSignatureInvalid, not ErrIndexMalformed)", err)
	}
	assertNoCachedIndex(t, dir)
}
