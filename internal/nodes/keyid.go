package nodes

// Purpose: KeyIDFor, the stable identifier of one Ed25519 public key
// (contract:ci-attestation). A node id stays fixed across rotation; the key
// id names which key signed, so a verifier can refuse a superseded one.
//
// Inputs: an Ed25519 public key.
// Outputs: the first 16 hex characters of SHA-256(pub).
// SPORT: internal.nodes.KeyIDFor/ADDED (P1-CI-04).

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
)

// keyIDLen is the length, in hex characters, of a key id.
const keyIDLen = 16

// KeyIDFor returns pub's key id: the first 16 hex characters of
// SHA-256(pub). Deterministic, and distinct for distinct keys with
// overwhelming probability.
func KeyIDFor(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:keyIDLen]
}
