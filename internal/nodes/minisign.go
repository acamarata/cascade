// Purpose: a real minisign (jedisct1/minisign) armored signature-file
//   parser and verifier — the format the §D-16 release train's
//   .goreleaser.yaml `minisign-checksums` sign step and
//   .github/workflows/release.yml's `minisign -Vm` verify step already
//   use for END-USER checksum verification. This ticket's node transfer
//   leg verifies the SAME signature format against the SAME artifact
//   class (§D-32): a transferred release binary is never installed or
//   executed unless it verifies here.
// Inputs: the two lines minisign's signature file carries after the
//   comment header (base64 sig-and-comment data, base64 global
//   signature) plus a minisign public key file's single base64 line; the
//   message bytes being verified are the caller's, never re-derived from
//   the signature itself (Art.2 — never verify an artifact against a
//   hash the artifact itself supplied).
// Outputs: a parsed MinisignSignature/MinisignPublicKey, or a
//   KindInvalidInput refusal for anything malformed; VerifyMinisign
//   returns nil only when BOTH the message signature and the
//   comment-binding global signature verify, KindIntegrity otherwise.
// Constraints: FAIL CLOSED on every path — wrong line count, bad base64,
//   wrong-length fields, an unrecognized signature algorithm, a key-id
//   mismatch between the signature and the public key, or either
//   signature failing to verify all refuse. Supports minisign's two real
//   wire algorithms: "Ed" (legacy, direct ed25519 over the message) and
//   "ED" (current default, ed25519 over the message's BLAKE2b-512
//   digest) — both confirmed byte-for-byte against the real minisign
//   0.12 CLI (internal/nodes/testdata/README.md provenance). No other
//   algorithm tag exists in the format; anything else is malformed input.
// SPORT: internal/nodes MinisignSignature/ADDED, VerifyMinisign/ADDED
//   (P1-E17-W4-S36-T5).

package nodes

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/blake2b"

	"github.com/acamarata/cascade/pkg/cascade"
)

// minisignAlgoLegacy is minisign's direct (non-prehashed) ed25519 mode.
const minisignAlgoLegacy = "Ed"

// minisignAlgoPrehashed is minisign's default mode since it began
// BLAKE2b-512-prehashing large files before signing.
const minisignAlgoPrehashed = "ED"

// minisignSigDataLen is the decoded length of a signature file's first
// base64 line: 2-byte algorithm tag + 8-byte key id + 64-byte ed25519
// signature.
const minisignSigDataLen = 74

// minisignPubDataLen is the decoded length of a minisign public-key
// file's single base64 line: 2-byte algorithm tag + 8-byte key id +
// 32-byte ed25519 public key.
const minisignPubDataLen = 42

const untrustedCommentPrefix = "untrusted comment: "
const trustedCommentPrefix = "trusted comment: "

// MinisignSignature is a parsed minisign detached signature file.
type MinisignSignature struct {
	// Algorithm is "Ed" or "ED" (ParseMinisignSignature refuses any other
	// value).
	Algorithm string
	// KeyID is the 8-byte key identifier the signing key stamped.
	KeyID [8]byte
	// Signature is the raw 64-byte ed25519 signature over the message
	// (or, in "ED" mode, over the message's BLAKE2b-512 digest).
	Signature [64]byte
	// TrustedComment is the human-readable comment the global signature
	// binds to the message signature, defeating a swapped-comment attack.
	TrustedComment string
	// GlobalSignature is the raw 64-byte ed25519 signature over
	// (the 74-byte sig-data blob || TrustedComment), per the real
	// minisign wire format this file's package doc records the
	// provenance for.
	GlobalSignature [64]byte
}

// MinisignPublicKey is a parsed minisign public-key file.
type MinisignPublicKey struct {
	Algorithm string
	KeyID     [8]byte
	Key       ed25519.PublicKey
}

// errMinisignMalformed reports a fail-closed parse refusal.
func errMinisignMalformed(reason string) error {
	return cascade.Newf(cascade.KindInvalidInput, "nodes: minisign signature malformed: %s", reason)
}

// ParseMinisignSignature parses a minisign detached signature file's raw
// bytes (the FuzzMinisignSignature target's subject). It never trusts
// line count, base64 shape, or field lengths implicitly: every deviation
// from the exact four-line armored format refuses.
func ParseMinisignSignature(data []byte) (MinisignSignature, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 4 {
		return MinisignSignature{}, errMinisignMalformed("expected exactly 4 lines (untrusted comment, sig data, trusted comment, global signature)")
	}
	if !strings.HasPrefix(lines[0], untrustedCommentPrefix) {
		return MinisignSignature{}, errMinisignMalformed("line 1 is not an untrusted-comment line")
	}
	if !strings.HasPrefix(lines[2], trustedCommentPrefix) {
		return MinisignSignature{}, errMinisignMalformed("line 3 is not a trusted-comment line")
	}

	sigData, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return MinisignSignature{}, errMinisignMalformed("line 2 is not valid base64")
	}
	if len(sigData) != minisignSigDataLen {
		return MinisignSignature{}, errMinisignMalformed("signature data is not 74 bytes")
	}
	algo := string(sigData[0:2])
	if algo != minisignAlgoLegacy && algo != minisignAlgoPrehashed {
		return MinisignSignature{}, errMinisignMalformed("unrecognized signature algorithm tag " + algo)
	}

	globalSig, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil {
		return MinisignSignature{}, errMinisignMalformed("line 4 is not valid base64")
	}
	if len(globalSig) != ed25519.SignatureSize {
		return MinisignSignature{}, errMinisignMalformed("global signature is not 64 bytes")
	}

	sig := MinisignSignature{
		Algorithm:      algo,
		TrustedComment: strings.TrimPrefix(lines[2], trustedCommentPrefix),
	}
	copy(sig.KeyID[:], sigData[2:10])
	copy(sig.Signature[:], sigData[10:74])
	copy(sig.GlobalSignature[:], globalSig)
	return sig, nil
}

// ParseMinisignPublicKey parses a minisign public-key file's raw bytes
// (untrusted-comment line + one base64 line).
func ParseMinisignPublicKey(data []byte) (MinisignPublicKey, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		return MinisignPublicKey{}, errMinisignMalformed("public key: expected exactly 2 lines")
	}
	pubData, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return MinisignPublicKey{}, errMinisignMalformed("public key: line 2 is not valid base64")
	}
	if len(pubData) != minisignPubDataLen {
		return MinisignPublicKey{}, errMinisignMalformed("public key: expected 42 decoded bytes")
	}
	algo := string(pubData[0:2])
	if algo != minisignAlgoLegacy && algo != minisignAlgoPrehashed {
		return MinisignPublicKey{}, errMinisignMalformed("public key: unrecognized algorithm tag " + algo)
	}
	key := MinisignPublicKey{Algorithm: algo, Key: ed25519.PublicKey(append([]byte(nil), pubData[10:42]...))}
	copy(key.KeyID[:], pubData[2:10])
	return key, nil
}

// ErrSignatureInvalid (KindIntegrity) reports that a minisign signature
// failed to verify: an unmatched key id, a message signature that does
// not verify, or a global (comment-binding) signature that does not
// verify. Callers MUST treat every one of these identically: refuse,
// never install, never execute (§D-32).
func ErrSignatureInvalid(reason string) error {
	return cascade.Newf(cascade.KindIntegrity, "nodes: minisign verification failed: %s", reason)
}

// VerifyMinisign verifies sig over message using pub, per the real
// minisign wire semantics (internal/nodes/testdata/README.md provenance):
//  1. sig.KeyID must equal pub.KeyID (a signature from a different key
//     never silently "verifies" against the wrong key).
//  2. the message signature verifies against message directly ("Ed") or
//     against blake2b-512(message) ("ED").
//  3. the global signature verifies against (the 74-byte sig-data blob
//     || TrustedComment) — binding the human-readable comment to the
//     message signature so a swapped trusted comment is detected.
//
// Every failure is ErrSignatureInvalid (KindIntegrity): this function
// never distinguishes "which check failed" in its return type, so a
// caller cannot be tempted to treat one failure mode as softer than
// another (12-QUALITY-CONSTITUTION Art.1/Art.3 fail-closed discipline).
func VerifyMinisign(pub MinisignPublicKey, message []byte, sig MinisignSignature) error {
	if sig.KeyID != pub.KeyID {
		return ErrSignatureInvalid("signature key id does not match the verifying public key")
	}
	if len(pub.Key) != ed25519.PublicKeySize {
		return ErrSignatureInvalid("public key is not a valid ed25519 key")
	}

	toVerify := message
	if sig.Algorithm == minisignAlgoPrehashed {
		digest := blake2b.Sum512(message)
		toVerify = digest[:]
	}
	if !ed25519.Verify(pub.Key, toVerify, sig.Signature[:]) {
		return ErrSignatureInvalid("message signature does not verify")
	}

	// The global signature covers only the raw 64-byte ed25519 signature
	// plus the trusted comment — NOT the 2-byte algorithm tag or 8-byte
	// key id that precede it in the sig-data blob (confirmed against the
	// real minisign 0.12 CLI; see the package doc's provenance note).
	globalMsg := append(append([]byte{}, sig.Signature[:]...), sig.TrustedComment...)
	if !ed25519.Verify(pub.Key, globalMsg, sig.GlobalSignature[:]) {
		return ErrSignatureInvalid("global (trusted comment) signature does not verify")
	}
	return nil
}
