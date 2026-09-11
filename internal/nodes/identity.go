// Purpose: the node identity model: a per-node Ed25519 keypair and the
//
//	stable node id derived from its public key.
//
// Inputs: crypto/rand entropy (production) or an injected io.Reader
//
//	(tests) for key generation; raw bytes/strings from untrusted sources
//	for parsing.
//
// Outputs: an Identity (public key + node id), or a typed fail-closed
//
//	error for anything malformed or unparseable.
//
// Constraints: no custom crypto — Ed25519 via crypto/ed25519, the same
//
//	primitive internal/elevation and internal/secrets already use in
//	this tree. Private key material is never held by this type: Identity
//	carries only the public half; the private half lives exclusively in
//	the OS keystore (keystore.go, R-16.44 custody) and is handled by
//	NodeKeystore.Sign, never returned to a caller.
//
// SPORT: internal/nodes Identity/ADDED (P1-E17-W4-S36-T1).

package nodes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// NodeIDLen is the length, in hex characters, of a stable node id: the
// first 16 bytes (32 hex chars) of SHA-256(pubkey), which is short enough
// to be usable on a command line while keeping collision probability
// negligible for any realistic fleet size.
const NodeIDLen = 32

// Identity is one node's public identity: its Ed25519 public key and the
// stable node id derived from it. It never carries private key material.
type Identity struct {
	// NodeID is the stable identifier derived from PubKey (DeriveNodeID).
	NodeID string
	// PubKey is the raw Ed25519 public key.
	PubKey ed25519.PublicKey
}

// DeriveNodeID computes the stable node id for a public key: the first
// 16 bytes of SHA-256(pubkey), hex-encoded. Deterministic, so the same key
// always yields the same id and two different keys yield different ids
// with overwhelming probability.
func DeriveNodeID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:NodeIDLen/2])
}

// GenerateIdentity creates a fresh Ed25519 keypair from rnd (crypto/rand
// in production; an injected deterministic reader in tests) and returns
// the resulting Identity plus the raw private key. The private key is
// returned ONLY so the caller (enroll.go) can hand it to NodeKeystore.Store
// in the same call chain that generated it; nothing in this package
// retains it beyond that.
func GenerateIdentity(rnd io.Reader) (Identity, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rnd)
	if err != nil {
		return Identity{}, nil, cascade.Wrap(cascade.KindInternal, err, "nodes: identity key generation failed")
	}
	return Identity{NodeID: DeriveNodeID(pub), PubKey: pub}, priv, nil
}

// ParsePublicKey fail-closed-validates a base64-standard-encoded Ed25519
// public key from an untrusted source (an enroll payload, a stored
// record). It is the single entry point every parser in this package uses
// to turn untrusted bytes into a PubKey: unparseable base64, the wrong
// key length, or an all-zero key (never a valid Ed25519 point in
// practice, and the classic "empty struct decoded as a value" bug shape)
// are all refused rather than silently accepted.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	if b64 == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: public key is required")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: public key is not valid base64")
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"nodes: public key has length %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	if allZero(raw) {
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: public key is all-zero")
	}
	return ed25519.PublicKey(raw), nil
}

// ValidateIdentity fail-closed-validates an untrusted Identity: the
// public key must parse (ParsePublicKey's rules) and NodeID must match
// DeriveNodeID(PubKey) exactly. A caller-supplied node id that does not
// match its own key's derivation is refused, never silently
// recomputed — accepting a mismatched pair would let an attacker claim
// an id that belongs to someone else's key.
func ValidateIdentity(nodeID string, pubKeyB64 string) (Identity, error) {
	pub, err := ParsePublicKey(pubKeyB64)
	if err != nil {
		return Identity{}, err
	}
	if nodeID == "" {
		return Identity{}, cascade.New(cascade.KindInvalidInput, "nodes: node id is required")
	}
	want := DeriveNodeID(pub)
	if nodeID != want {
		return Identity{}, cascade.Newf(cascade.KindInvalidInput,
			"nodes: node id %q does not match its public key's derived id %q", nodeID, want)
	}
	return Identity{NodeID: nodeID, PubKey: pub}, nil
}

// PubKeyB64 returns id's public key, base64-standard-encoded, the wire
// shape every payload and record in this package uses.
func (id Identity) PubKeyB64() string {
	return base64.StdEncoding.EncodeToString(id.PubKey)
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
