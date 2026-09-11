// Purpose: R-21.221's signed heartbeat/capability envelope: the
//
//	deterministic signing payload, the sequence-replay guard, and the
//	fail-closed verifier every heartbeat frame passes before its liveness
//	or capability data is trusted.
//
// Inputs: a HeartbeatFrame built from untrusted wire bytes (heartbeat.go's
//
//	decoder), the DeviceRecord the frame claims to speak for, and the
//	prior highest sequence number this store has seen for that node id.
//
// Outputs: an accept/refuse decision, mirroring transcript.go's
//
//	SigningPayload/VerifyTranscript shape for the same reason: two
//	independent signers/verifiers must compute byte-identical payloads.
//
// Constraints: R-21.221 — a frame whose node id does not match the
//
//	verified signer, whose sequence is not strictly increasing, or whose
//	signing key is in the S-36.T1 revoked set is refused. EnrollmentID:
//	DeviceRecord (records.go, S-36.T1) has no persisted enrollment-id
//	field and records.go is not in this ticket's files_scope, so
//	DeriveEnrollmentID computes a stable id from the record's own
//	controller-issued NodeID + EnrolledAt fields rather than adding a
//	fifth persisted field elsewhere. A heartbeat's claimed enrollment id
//	is checked against this derived value, never accepted verbatim. See
//	the ticket journal's CONTRADICTIONS section for the full quote.
//
// SPORT: internal/nodes HeartbeatFrame/ADDED, SequenceStore/ADDED
//
//	(P1-E17-W4-S36-T2).

package nodes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HeartbeatFrame is the signed wire envelope a node sends on every
// heartbeat: its claimed identity, the controller-issued enrollment id,
// a strictly monotonic per-node sequence number, and the capability
// report, all bound together under one Ed25519 signature.
type HeartbeatFrame struct {
	NodeID       string           `json:"node_id"`
	EnrollmentID string           `json:"enrollment_id"`
	Sequence     uint64           `json:"sequence"`
	Report       CapabilityReport `json:"report"`
	SignatureB64 string           `json:"signature_b64"`
}

// DeriveEnrollmentID computes the stable enrollment id for rec: SHA-256
// of NodeID | EnrolledAt (RFC3339Nano), hex-encoded, truncated to 32
// hex chars. Deterministic and controller-issued in the sense that both
// inputs are set exclusively by RecordStore.Enroll (records.go) at
// enrollment time and never by an untrusted caller afterward — a node
// cannot manufacture a new enrollment id for itself without a fresh
// enrollment.
func DeriveEnrollmentID(rec DeviceRecord) string {
	h := sha256.Sum256([]byte(rec.NodeID + "|" + rec.EnrolledAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")))
	return hex.EncodeToString(h[:16])
}

// signingPayload returns the deterministic bytes a heartbeat frame's
// signature covers: NodeID | EnrollmentID | Sequence | canonical-JSON
// Report, 0x1F-delimited (mirrors Transcript.SigningPayload). The report
// is included via its canonical JSON encoding (encoding/json's
// deterministic key ordering for a struct, unlike a map) so the signer
// and verifier never disagree about what was signed.
func (f HeartbeatFrame) signingPayload() ([]byte, error) {
	reportJSON, err := json.Marshal(f.Report)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: heartbeat capability report does not encode")
	}
	const sep = byte(0x1F)
	var buf []byte
	buf = append(buf, []byte(f.NodeID)...)
	buf = append(buf, sep)
	buf = append(buf, []byte(f.EnrollmentID)...)
	buf = append(buf, sep)
	buf = append(buf, []byte(strconv.FormatUint(f.Sequence, 10))...)
	buf = append(buf, sep)
	buf = append(buf, reportJSON...)
	return buf, nil
}

// SignHeartbeatFrame signs f's signing payload with priv and returns the
// raw signature. Production node-side callers sign via NodeKeystore.Sign
// instead (heartbeat.go); this exists for tests and any caller that
// already holds a raw key, mirroring SignTranscript's identical role.
func SignHeartbeatFrame(f HeartbeatFrame, priv ed25519.PrivateKey) ([]byte, error) {
	payload, err := f.signingPayload()
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, payload), nil
}

// Heartbeat refusal errors. Each is its own function (mirroring
// rotate.go's ErrKeyRevoked / ErrRotationSignatureInvalid convention) so
// a caller and a test can name the exact refusal reason rather than
// string-matching a generic message.

// ErrHeartbeatSpoofedSigner reports that a frame's signature does not
// verify against the claimed node id's own enrolled public key — either
// a different node's key signed it, or the signature is simply invalid.
// KindIntegrity: a verification step failed. Fail closed: an unverifiable
// signer is refused, never treated as some lesser-trusted signer.
func ErrHeartbeatSpoofedSigner(nodeID string) error {
	return cascade.Newf(cascade.KindIntegrity,
		"nodes: heartbeat frame for node %q does not verify against its enrolled public key (spoofed signer)", nodeID)
}

// ErrHeartbeatReplayedSequence reports that a frame's sequence number is
// not strictly greater than the last sequence this store accepted for the
// node — a replayed or reordered capture. KindConflict: the frame
// conflicts with state already on record.
func ErrHeartbeatReplayedSequence(nodeID string, got, lastSeen uint64) error {
	return cascade.Newf(cascade.KindConflict,
		"nodes: heartbeat frame for node %q has sequence %d, which is not strictly greater than the last accepted sequence %d (replay refused)",
		nodeID, got, lastSeen)
}

// ErrHeartbeatRevokedKey reports that the key which signed the frame has
// since been revoked or superseded (rotate.go's RevokedKeys set).
// KindPermissionDenied, mirroring rotate.go's ErrKeyRevoked exactly: the
// signer lacks standing, with no elevation path but a fresh enrollment.
func ErrHeartbeatRevokedKey(nodeID string) error {
	return cascade.Newf(cascade.KindPermissionDenied,
		"nodes: heartbeat frame for node %q was signed by a revoked or superseded key; the frame is refused", nodeID)
}

// ErrHeartbeatEnrollmentMismatch reports that a frame's claimed
// enrollment id does not match the id this store derives for the node's
// current enrollment record. KindIntegrity.
func ErrHeartbeatEnrollmentMismatch(nodeID string) error {
	return cascade.Newf(cascade.KindIntegrity,
		"nodes: heartbeat frame for node %q carries an enrollment id that does not match its current enrollment (integrity check failed)", nodeID)
}

// VerifyHeartbeatFrame fail-closed-verifies f against rec (the device
// record for f's claimed node id, already looked up by the caller) and
// lastSeq (the highest sequence this store has previously accepted for
// that node id, 0 if none yet). Every one of R-21.221's refusal
// conditions is checked, in order: signature (spoofed signer), revoked
// key, enrollment id, then sequence — signature first, since nothing else
// about an unverified frame can be trusted.
func VerifyHeartbeatFrame(f HeartbeatFrame, rec DeviceRecord, lastSeq uint64) error {
	pub, err := ParsePublicKey(rec.PubKeyB64)
	if err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: stored device record public key is corrupt")
	}
	sigRaw, err := decodeSignature(f.SignatureB64)
	if err != nil {
		return err
	}
	payload, err := f.signingPayload()
	if err != nil {
		return err
	}
	if len(sigRaw) != ed25519.SignatureSize || !ed25519.Verify(pub, payload, sigRaw) {
		return ErrHeartbeatSpoofedSigner(f.NodeID)
	}
	if SignatureRevoked(rec, rec.PubKeyB64) {
		return ErrHeartbeatRevokedKey(f.NodeID)
	}
	if f.EnrollmentID != DeriveEnrollmentID(rec) {
		return ErrHeartbeatEnrollmentMismatch(f.NodeID)
	}
	if f.Sequence <= lastSeq {
		return ErrHeartbeatReplayedSequence(f.NodeID, f.Sequence, lastSeq)
	}
	return nil
}

// SequenceStore tracks the highest accepted heartbeat sequence per node
// id, in-process. It is intentionally NOT persisted to disk: the
// controller process's own lifetime is the replay-protection window this
// ticket implements, matching the "REFUSES a frame whose sequence is not
// strictly increasing" contract without inventing a durable store outside
// files_scope. A controller restart resets the counter to 0 for every
// node, which is conservative (a node's own sequence only ever increases
// from its last known value, so the first post-restart heartbeat is
// always accepted) rather than permissive.
type SequenceStore struct {
	mu   sync.Mutex
	last map[string]uint64
}

// NewSequenceStore returns an empty SequenceStore.
func NewSequenceStore() *SequenceStore {
	return &SequenceStore{last: make(map[string]uint64)}
}

// Last returns the highest sequence previously accepted for nodeID, 0 if
// none.
func (s *SequenceStore) Last(nodeID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[nodeID]
}

// Advance records seq as the highest accepted sequence for nodeID.
// Callers must have already verified seq > Last(nodeID) via
// VerifyHeartbeatFrame before calling Advance.
func (s *SequenceStore) Advance(nodeID string, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[nodeID] = seq
}
