package nodes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the signed envelope a dispatch acknowledgement or
//
//	result rides back on (R-21.221), and its verification.
//
// Inputs: a frame and the claimed node's enrolled device record.
// Outputs: a verified frame, or a typed refusal.
// Constraints: this deliberately mirrors heartbeat_sign.go rather than
//
//	inventing a second envelope — same fields bound under one signature,
//	same 0x1F-delimited deterministic payload, same fail-closed posture. A
//	second signing scheme in one package is a second thing to get wrong,
//	and the contract says dispatch frames ride the S-36.T2 envelope.
//
//	The ATTEMPT is inside the signed payload, not beside it. That is what
//	makes fencing unforgeable: a partitioned node cannot relabel its stale
//	result as the current attempt without invalidating its own signature.
//
// SPORT: internal/nodes:dispatch-frame (ADD) — P1-E17-W4-S37-T2.

// DispatchOutcome is the terminal state a node reports for an attempt.
type DispatchOutcome string

const (
	// OutcomeAccepted acknowledges receipt, before any work runs.
	OutcomeAccepted DispatchOutcome = "accepted"
	// OutcomeSucceeded reports the work finished and results were pushed.
	OutcomeSucceeded DispatchOutcome = "succeeded"
	// OutcomeFailed reports the work ran and failed.
	OutcomeFailed DispatchOutcome = "failed"
	// OutcomeRefused reports the node declined before running anything —
	// a duplicate action id, most often.
	OutcomeRefused DispatchOutcome = "refused"
)

// DispatchFrame is the signed envelope a node returns for one attempt.
type DispatchFrame struct {
	NodeID       string          `json:"node_id"`
	EnrollmentID string          `json:"enrollment_id"`
	Sequence     uint64          `json:"sequence"`
	DispatchID   string          `json:"dispatch_id"`
	Attempt      uint64          `json:"attempt"`
	ActionID     string          `json:"action_id"`
	Outcome      DispatchOutcome `json:"outcome"`
	// ResultCommit is the commit the node pushed results at, empty until
	// there are results.
	ResultCommit string `json:"result_commit,omitempty"`
	SignatureB64 string `json:"signature_b64"`
}

// signingPayload returns the bytes this frame's signature covers.
//
// Every field that decides what the frame MEANS is in here — node,
// enrollment, sequence, dispatch, attempt, action, outcome and result
// commit. A field outside the payload is a field an attacker may rewrite
// in transit, which for the attempt would defeat fencing and for the
// result commit would redirect the controller to arbitrary content.
func (f DispatchFrame) signingPayload() []byte {
	const sep = byte(0x1F)
	var buf []byte
	for _, part := range []string{
		f.NodeID,
		f.EnrollmentID,
		strconv.FormatUint(f.Sequence, 10),
		f.DispatchID,
		strconv.FormatUint(f.Attempt, 10),
		f.ActionID,
		string(f.Outcome),
		f.ResultCommit,
	} {
		buf = append(buf, []byte(part)...)
		buf = append(buf, sep)
	}
	return buf
}

// FrameSigner produces a detached signature over payload as nodeID.
//
// It is a function rather than a key so the node signs through
// NodeKeystore.Sign, which never returns the private key to a caller
// (keystore.go's custody contract). A test passes a closure over
// ed25519.Sign; production passes the keystore. There is deliberately ONE
// signing entry point, so the path a test exercises is the path that ships.
type FrameSigner func(ctx context.Context, nodeID string, payload []byte) ([]byte, error)

// SignDispatchFrame returns f with SignatureB64 set.
func SignDispatchFrame(ctx context.Context, f DispatchFrame, sign FrameSigner) (DispatchFrame, error) {
	if sign == nil {
		return DispatchFrame{}, cascade.New(cascade.KindInternal,
			"nodes: no signer was wired for the dispatch frame")
	}
	sig, err := sign(ctx, f.NodeID, f.signingPayload())
	if err != nil {
		return DispatchFrame{}, cascade.Wrapf(cascade.KindUnavailable, err,
			"nodes: signing the dispatch frame for %s", f.DispatchID)
	}
	f.SignatureB64 = base64.StdEncoding.EncodeToString(sig)
	return f, nil
}

// VerifyDispatchFrame checks a returned frame against the node's enrolled
// record and the attempt the controller believes is current.
//
// The order of the checks is the contract. Identity is proved FIRST: until
// the signature verifies, every other field is attacker-controlled and
// reasoning about the attempt number in an unverified frame would be
// reasoning about a value the attacker chose. Only then does fencing
// apply.
func VerifyDispatchFrame(f DispatchFrame, rec DeviceRecord, lastSeq, currentAttempt uint64) error {
	if f.NodeID == "" || f.NodeID != rec.NodeID {
		return ErrUnverifiedSigner(f.NodeID)
	}
	pub, err := ParsePublicKey(rec.PubKeyB64)
	if err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: stored device record public key is corrupt")
	}
	sig, err := decodeSignature(f.SignatureB64)
	if err != nil {
		return err
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, f.signingPayload(), sig) {
		return ErrUnverifiedSigner(f.NodeID)
	}
	// Same revocation and enrollment checks VerifyHeartbeatFrame makes,
	// in the same order and through the same helpers — a dispatch frame
	// from a revoked key or a stale enrollment is refused exactly as a
	// heartbeat from one would be.
	if SignatureRevoked(rec, rec.PubKeyB64) {
		return ErrHeartbeatRevokedKey(f.NodeID)
	}
	if f.EnrollmentID != DeriveEnrollmentID(rec) {
		return ErrHeartbeatEnrollmentMismatch(f.NodeID)
	}
	if f.Sequence <= lastSeq {
		return ErrHeartbeatReplayedSequence(f.NodeID, f.Sequence, lastSeq)
	}
	// Identity is proved; fencing can now be trusted.
	if f.Attempt != currentAttempt {
		return ErrStaleAttemptf(f.DispatchID, f.Attempt, currentAttempt)
	}
	if !validOutcome(f.Outcome) {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: dispatch %s reported an unrecognized outcome %q", f.DispatchID, f.Outcome)
	}
	return nil
}

// validOutcome reports whether outcome is one this build understands.
// Fail-closed: an unrecognized outcome is refused rather than treated as
// a success or quietly ignored.
func validOutcome(outcome DispatchOutcome) bool {
	switch outcome {
	case OutcomeAccepted, OutcomeSucceeded, OutcomeFailed, OutcomeRefused:
		return true
	default:
		return false
	}
}
