package plugin

// Purpose: the production RegistryVerifier — pure-Go Ed25519 signature
//   verification of the index envelope's embedded Signature field, with
//   no CGO and no network. See testdata/registry/README.md for why this
//   client verifies a stdlib Ed25519 signature rather than parsing the
//   minisign wire format the ticket's prose describes, and
//   docs_updates/journal for the contradiction this resolves.
// Inputs: raw index bytes as fetched or read from cache.
// Outputs: a verified RegistryIndex, or a KindIntegrity/KindInvalidInput
//   *cascade.Error — never a partially trusted result.
// Constraints: fail closed on every path — empty signature, malformed
//   base64, wrong-length signature, wrong-length public key, and a
//   signature that does not verify all refuse (12-QUALITY-CONSTITUTION.md
//   Art.1, Art.3). Entries are decoded from the ORIGINAL json.RawMessage
//   the signature was computed over, not re-marshaled first, closing the
//   re-encoding-drift gap a naive "unmarshal then re-marshal to verify"
//   implementation would open.
// SPORT: pkg/plugin registry-client (ADD) — P1-E24-W5-S50-T1.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SchemaVersionCurrent is the only RegistryIndex.SchemaVersion value this
// client accepts. A payload whose schema_version is missing or unequal to
// this constant is refused with ErrIndexMalformed.
const SchemaVersionCurrent = "1"

// Ed25519Verifier is the production RegistryVerifier. The zero value is
// not usable: PublicKey must be set to a 32-byte Ed25519 public key, or
// every verification fails closed with ErrSignatureInvalid.
type Ed25519Verifier struct {
	// PublicKey is the registry's Ed25519 public key.
	PublicKey ed25519.PublicKey
}

var _ RegistryVerifier = Ed25519Verifier{}

// rawIndexEnvelope splits the top-level JSON document into its three
// fields without touching the byte range Entries occupies, so that range
// can be re-used verbatim as part of the signed payload.
type rawIndexEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Entries       json.RawMessage `json:"entries"`
	Signature     string          `json:"signature"`
}

// signedPayload is exactly what the registry signs: schema_version plus
// the raw entries bytes, re-encoded with fixed field order and no other
// fields. It deliberately excludes Signature itself.
type signedPayload struct {
	SchemaVersion string          `json:"schema_version"`
	Entries       json.RawMessage `json:"entries"`
}

// VerifyIndex implements RegistryVerifier.
func (v Ed25519Verifier) VerifyIndex(_ context.Context, data []byte) (RegistryIndex, error) {
	var env rawIndexEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return RegistryIndex{}, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: unmarshal index envelope", ErrIndexMalformed.Msg)
	}
	if env.SchemaVersion == "" || env.SchemaVersion != SchemaVersionCurrent {
		return RegistryIndex{}, cascade.Newf(cascade.KindInvalidInput, "%s: unsupported schema_version %q", ErrIndexMalformed.Msg, env.SchemaVersion)
	}
	if len(v.PublicKey) != ed25519.PublicKeySize {
		return RegistryIndex{}, cascade.Newf(cascade.KindIntegrity, "%s: verifier public key is %d bytes, want %d", ErrSignatureInvalid.Msg, len(v.PublicKey), ed25519.PublicKeySize)
	}
	if env.Signature == "" {
		return RegistryIndex{}, cascade.New(cascade.KindIntegrity, ErrSignatureInvalid.Msg+": index has no signature")
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return RegistryIndex{}, cascade.New(cascade.KindIntegrity, ErrSignatureInvalid.Msg+": signature is not a valid ed25519 signature")
	}

	payload, err := json.Marshal(signedPayload{SchemaVersion: env.SchemaVersion, Entries: env.Entries})
	if err != nil {
		return RegistryIndex{}, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: re-encode signed payload", ErrIndexMalformed.Msg)
	}
	if !ed25519.Verify(v.PublicKey, payload, sig) {
		return RegistryIndex{}, cascade.New(cascade.KindIntegrity, ErrSignatureInvalid.Msg+": signature does not verify")
	}

	var entries []RegistryIndexEntry
	if err := json.Unmarshal(env.Entries, &entries); err != nil {
		return RegistryIndex{}, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: unmarshal verified entries", ErrIndexMalformed.Msg)
	}
	return RegistryIndex{SchemaVersion: env.SchemaVersion, Entries: entries, Signature: env.Signature}, nil
}

// VerifyArtifact implements RegistryVerifier: it checks data's SHA-256
// digest against entry.Checksum via VerifyArtifact (the exported
// standalone helper), then, if entry carries a non-empty Signature, also
// verifies that signature against data with the same public key.
func (v Ed25519Verifier) VerifyArtifact(_ context.Context, data []byte, entry RegistryVersionEntry) error {
	if err := VerifyArtifact(entry, data); err != nil {
		return err
	}
	if entry.Signature == "" {
		return nil
	}
	if len(v.PublicKey) != ed25519.PublicKeySize {
		return cascade.Newf(cascade.KindIntegrity, "%s: verifier public key is %d bytes, want %d", ErrSignatureInvalid.Msg, len(v.PublicKey), ed25519.PublicKeySize)
	}
	sig, err := base64.StdEncoding.DecodeString(entry.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return cascade.New(cascade.KindIntegrity, ErrSignatureInvalid.Msg+": artifact signature is not a valid ed25519 signature")
	}
	if !ed25519.Verify(v.PublicKey, data, sig) {
		return cascade.New(cascade.KindIntegrity, ErrSignatureInvalid.Msg+": artifact signature does not verify")
	}
	return nil
}
