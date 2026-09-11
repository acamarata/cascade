// Purpose: the enrollment transcript (R-21.220) that binds the pinned ssh
//
//	host key into a payload both the node and controller identity keys
//	sign, and the verifier every enrollment must pass before a device
//	record is created.
//
// Inputs: the two Ed25519 public keys (node, controller), the pinned host
//
//	key fingerprint, and both signatures.
//
// Outputs: an accept/refuse decision. A transcript whose signature or
//
//	pinned host key fails verification is refused (contract text,
//	verbatim).
//
// Constraints: no custom crypto — crypto/ed25519.Verify only, the same
//
//	primitive identity.go's keys already are. The signed payload is a
//	deterministic byte encoding of the transcript's fields (never the
//	Go struct's json.Marshal output, whose key order is unspecified by
//	the encoding/json contract) so two independent signers/verifiers
//	compute byte-identical payloads.
//
// SPORT: internal/nodes Transcript/ADDED (P1-E17-W4-S36-T1).

package nodes

import (
	"crypto/ed25519"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Transcript is the enrollment handshake's mutually-signed record: it
// binds the node identity, the controller identity, and the pinned ssh
// host-key fingerprint into one payload both parties sign.
type Transcript struct {
	NodeID              string
	NodePubKeyB64       string
	ControllerPubKeyB64 string
	HostKeyFingerprint  string
	// NodeSignature is the node identity key's signature over
	// SigningPayload().
	NodeSignature []byte
	// ControllerSignature is the controller identity key's signature over
	// the identical SigningPayload().
	ControllerSignature []byte
}

// SigningPayload returns the deterministic byte sequence both signers
// sign: NodeID | NodePubKeyB64 | ControllerPubKeyB64 | HostKeyFingerprint,
// each field length-prefix-delimited with a single 0x1F separator so no
// concatenation ambiguity exists between adjacent fields (e.g. NodeID
// "ab" + PubKey "cd" is never confusable with NodeID "abc" + PubKey "d").
func (t Transcript) SigningPayload() []byte {
	const sep = byte(0x1F)
	var buf []byte
	buf = append(buf, []byte(t.NodeID)...)
	buf = append(buf, sep)
	buf = append(buf, []byte(t.NodePubKeyB64)...)
	buf = append(buf, sep)
	buf = append(buf, []byte(t.ControllerPubKeyB64)...)
	buf = append(buf, sep)
	buf = append(buf, []byte(t.HostKeyFingerprint)...)
	return buf
}

// ErrTranscriptSignatureInvalid reports that a transcript's node or
// controller signature does not verify against its own claimed public key
// over SigningPayload(). KindIntegrity: a verification step failed.
func ErrTranscriptSignatureInvalid(who string) error {
	return cascade.Newf(cascade.KindIntegrity, "nodes: enrollment transcript %s signature failed verification", who)
}

// ErrTranscriptHostKeyMismatch reports that the transcript's bound
// HostKeyFingerprint does not match the fingerprint the caller
// independently pinned/verified via KnownHosts. KindIntegrity.
func ErrTranscriptHostKeyMismatch() error {
	return cascade.New(cascade.KindIntegrity, "nodes: enrollment transcript's bound host-key fingerprint does not match the pinned host key")
}

// VerifyTranscript fail-closed-verifies t: both signatures must verify
// against their claimed public keys over the identical SigningPayload,
// and t.HostKeyFingerprint must equal expectedHostKeyFingerprint (the
// fingerprint the caller independently confirmed via KnownHosts.Verify
// before ever accepting a transcript — this function never trusts the
// transcript's own claim about which host key was pinned).
func VerifyTranscript(t Transcript, expectedHostKeyFingerprint string) error {
	nodePub, err := ParsePublicKey(t.NodePubKeyB64)
	if err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: enrollment transcript node public key is invalid")
	}
	ctrlPub, err := ParsePublicKey(t.ControllerPubKeyB64)
	if err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: enrollment transcript controller public key is invalid")
	}
	if t.HostKeyFingerprint == "" || t.HostKeyFingerprint != expectedHostKeyFingerprint {
		return ErrTranscriptHostKeyMismatch()
	}
	payload := t.SigningPayload()
	if len(t.NodeSignature) != ed25519.SignatureSize || !ed25519.Verify(nodePub, payload, t.NodeSignature) {
		return ErrTranscriptSignatureInvalid("node")
	}
	if len(t.ControllerSignature) != ed25519.SignatureSize || !ed25519.Verify(ctrlPub, payload, t.ControllerSignature) {
		return ErrTranscriptSignatureInvalid("controller")
	}
	return nil
}

// SignTranscript signs t's SigningPayload with priv and returns the raw
// signature. Callers assign the result to either NodeSignature or
// ControllerSignature depending on which identity priv belongs to. In
// production, priv is never held directly by a caller of this function —
// enroll.go signs via NodeKeystore.Sign instead, which never returns the
// private key itself; this function exists for the in-process
// end-to-end test path and for any caller that already holds a raw key
// (e.g. a test fixture).
func SignTranscript(t Transcript, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, t.SigningPayload())
}
