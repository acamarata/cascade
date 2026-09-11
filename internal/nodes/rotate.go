// Purpose: identity rotation and revocation (R-21.220): `rotate` records a
//
//	new public key signed by the current identity key and moves the
//	superseded key into a revoked set while preserving the device record;
//	`revoke` invalidates every future attestation/heartbeat from a key.
//
// Inputs: the node id, the new public key (rotate) or nothing beyond the
//
//	node id (revoke), and a signature over the rotation request produced
//	by the CURRENT (not the new) identity key.
//
// Outputs: the updated DeviceRecord, or a typed fail-closed error.
// Constraints: R-21.220 — rotation preserves the device record, its
//
//	trust_tier and its sync cursors; a frame signed by a revoked or
//	superseded key is refused (RefusesSignature below is the check every
//	later heartbeat/handshake verifier calls). Both operations carry the
//	06 §5.14 elevated-verb class and are mounted by S-36.T4 (the CLI
//	surface); this file is the operation, not the verb.
//
// SPORT: internal/nodes Rotate/ADDED, Revoke/ADDED (P1-E17-W4-S36-T1).

package nodes

import (
	"crypto/ed25519"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrRotationSignatureInvalid reports that a rotation request's signature
// does not verify against the CURRENT identity key on record.
// KindIntegrity: a verification step failed.
func ErrRotationSignatureInvalid() error {
	return cascade.New(cascade.KindIntegrity, "nodes: rotation request signature does not verify against the current enrolled key")
}

// ErrKeyRevoked reports that a frame was signed by a key this node id has
// since revoked or superseded. KindPermissionDenied: the signer lacks the
// standing this operation requires, with no elevation path — the only way
// back in is a fresh enrollment under a new identity.
func ErrKeyRevoked(nodeID string) error {
	return cascade.Newf(cascade.KindPermissionDenied, "nodes: node %q's signing key has been revoked or superseded; the frame is refused", nodeID)
}

// rotationSigningPayload returns the deterministic bytes a rotation
// request's signature covers: nodeID | newPubKeyB64, 0x1F-delimited
// (mirrors Transcript.SigningPayload's unambiguous-concatenation
// convention).
func rotationSigningPayload(nodeID, newPubKeyB64 string) []byte {
	buf := append([]byte(nil), []byte(nodeID)...)
	buf = append(buf, 0x1F)
	buf = append(buf, []byte(newPubKeyB64)...)
	return buf
}

// SignRotationRequest signs a rotation of nodeID to newPubKeyB64 with the
// CURRENT identity's private key. Production callers sign via
// NodeKeystore.Sign instead (the private key never leaves custody); this
// helper exists for tests and for any caller that already holds a raw key.
func SignRotationRequest(nodeID, newPubKeyB64 string, currentPriv ed25519.PrivateKey) []byte {
	return ed25519.Sign(currentPriv, rotationSigningPayload(nodeID, newPubKeyB64))
}

// Rotate records newPubKeyB64 as nodeID's active key, signed by the
// CURRENT key on record (proof of continuity), and moves the superseded
// key's fingerprint into the record's RevokedKeys set. The device
// record's trust_tier and SyncCursors are preserved unchanged — Rotate
// only ever touches PubKeyB64 and RevokedKeys.
func (s *RecordStore) Rotate(nodeID, newPubKeyB64 string, signature []byte) (DeviceRecord, error) {
	rec, err := s.Get(nodeID)
	if err != nil {
		return DeviceRecord{}, err
	}
	currentPub, err := ParsePublicKey(rec.PubKeyB64)
	if err != nil {
		return DeviceRecord{}, cascade.Wrap(cascade.KindIntegrity, err, "nodes: stored current public key is corrupt")
	}
	if containsFingerprint(rec.RevokedKeys, fingerprintOfKey(currentPub)) {
		return DeviceRecord{}, ErrKeyRevoked(nodeID)
	}
	newPub, err := ParsePublicKey(newPubKeyB64)
	if err != nil {
		return DeviceRecord{}, err
	}
	if len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(currentPub, rotationSigningPayload(nodeID, newPubKeyB64), signature) {
		return DeviceRecord{}, ErrRotationSignatureInvalid()
	}

	supersededFP := fingerprintOfKey(currentPub)
	rec.RevokedKeys = append(append([]string(nil), rec.RevokedKeys...), supersededFP)
	rec.PubKeyB64 = newPubKeyB64
	_ = newPub // parsed only to validate shape; the b64 string is what persists

	if err := s.put(rec); err != nil {
		return DeviceRecord{}, err
	}
	return rec, nil
}

// Revoke invalidates nodeID's current key outright: it is moved into
// RevokedKeys with no replacement key installed, so every subsequent
// SignatureRevoked check against it refuses. The device record itself
// (tier, sync cursors, enrollment history) is preserved, matching
// Rotate's preservation contract — `cascade node revoke` is the elevated
// CLI verb (S-36.T4) this operation backs.
func (s *RecordStore) Revoke(nodeID string) (DeviceRecord, error) {
	rec, err := s.Get(nodeID)
	if err != nil {
		return DeviceRecord{}, err
	}
	pub, err := ParsePublicKey(rec.PubKeyB64)
	if err != nil {
		return DeviceRecord{}, cascade.Wrap(cascade.KindIntegrity, err, "nodes: stored current public key is corrupt")
	}
	fp := fingerprintOfKey(pub)
	if !containsFingerprint(rec.RevokedKeys, fp) {
		rec.RevokedKeys = append(append([]string(nil), rec.RevokedKeys...), fp)
	}
	if err := s.put(rec); err != nil {
		return DeviceRecord{}, err
	}
	return rec, nil
}

// SignatureRevoked reports whether pubKeyB64 (the key that produced a
// given heartbeat/handshake signature) is in rec's revoked set — the
// check every consumer of a signed frame runs before trusting it. A
// corrupt or unparseable pubKeyB64 is treated as revoked (fail closed):
// an un-fingerprintable key can never be positively cleared.
func SignatureRevoked(rec DeviceRecord, pubKeyB64 string) bool {
	pub, err := ParsePublicKey(pubKeyB64)
	if err != nil {
		return true
	}
	return containsFingerprint(rec.RevokedKeys, fingerprintOfKey(pub))
}

func fingerprintOfKey(pub ed25519.PublicKey) string {
	return HostKeyFingerprint([]byte(pub))
}

func containsFingerprint(set []string, fp string) bool {
	for _, s := range set {
		if s == fp {
			return true
		}
	}
	return false
}
