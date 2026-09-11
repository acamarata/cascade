// Purpose: tests for the standalone VerifyArtifact checksum helper and
//
//	Ed25519Verifier.VerifyArtifact's signature-check layer over it.
//
// SPORT: pkg/plugin registry-client tests (ADD) — P1-E24-W5-S50-T1.
package plugin_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestVerifyArtifact_ChecksumMatch(t *testing.T) {
	data := []byte("artifact bytes")
	sum := sha256.Sum256(data)
	entry := plugin.RegistryVersionEntry{Checksum: hex.EncodeToString(sum[:])}

	if err := plugin.VerifyArtifact(entry, data); err != nil {
		t.Fatalf("VerifyArtifact(matching checksum): %v", err)
	}
}

func TestVerifyArtifact_ChecksumMismatch(t *testing.T) {
	entry := plugin.RegistryVersionEntry{Checksum: hex.EncodeToString(sha256.New().Sum(nil))}
	err := plugin.VerifyArtifact(entry, []byte("different bytes"))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact(mismatch) error = %v, want KindIntegrity", err)
	}
}

func TestVerifyArtifact_EmptyChecksumRefused(t *testing.T) {
	err := plugin.VerifyArtifact(plugin.RegistryVersionEntry{}, []byte("data"))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact(no checksum) error = %v, want KindIntegrity", err)
	}
}

func TestVerifyArtifact_NonHexChecksumRefused(t *testing.T) {
	err := plugin.VerifyArtifact(plugin.RegistryVersionEntry{Checksum: "not-hex!!"}, []byte("data"))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact(non-hex checksum) error = %v, want KindIntegrity", err)
	}
}

func TestEd25519Verifier_VerifyArtifact_ChecksumOnly(t *testing.T) {
	data := []byte("artifact bytes")
	sum := sha256.Sum256(data)
	entry := plugin.RegistryVersionEntry{Checksum: hex.EncodeToString(sum[:])}

	v := realVerifier(t)
	if err := v.VerifyArtifact(context.Background(), data, entry); err != nil {
		t.Fatalf("VerifyArtifact(checksum only): %v", err)
	}
}

func TestEd25519Verifier_VerifyArtifact_WithValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	data := []byte("signed artifact bytes")
	sum := sha256.Sum256(data)
	sig := ed25519.Sign(priv, data)
	entry := plugin.RegistryVersionEntry{
		Checksum:  hex.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}

	v := plugin.Ed25519Verifier{PublicKey: pub}
	if err := v.VerifyArtifact(context.Background(), data, entry); err != nil {
		t.Fatalf("VerifyArtifact(valid signature): %v", err)
	}
}

func TestEd25519Verifier_VerifyArtifact_WrongSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	other, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_ = other
	data := []byte("signed artifact bytes")
	sum := sha256.Sum256(data)
	// Signed by a DIFFERENT key than the verifier trusts.
	sig := ed25519.Sign(otherPriv, data)
	entry := plugin.RegistryVersionEntry{
		Checksum:  hex.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}

	v := plugin.Ed25519Verifier{PublicKey: pub}
	err = v.VerifyArtifact(context.Background(), data, entry)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact(wrong signature) error = %v, want KindIntegrity", err)
	}
}

func TestEd25519Verifier_VerifyArtifact_MalformedSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	data := []byte("data")
	sum := sha256.Sum256(data)
	entry := plugin.RegistryVersionEntry{Checksum: hex.EncodeToString(sum[:]), Signature: "not-base64!!"}

	v := plugin.Ed25519Verifier{PublicKey: pub}
	err = v.VerifyArtifact(context.Background(), data, entry)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact(malformed signature) error = %v, want KindIntegrity", err)
	}
}
