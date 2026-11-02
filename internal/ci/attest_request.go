package ci

// Purpose: the controller half of ci.attest.sign (contract:node-attest-sign):
// sign the request with the controller installation key the node pinned,
// call the node, and return its signed attestation once it is shown to
// answer this request.
// Inputs: an RPC call seam, the installation-key signer, the node id and the
// request (identifiers copied from the issued nonce fact).
// Outputs: an attest.SignedAttestation whose Record is decoded from its
// canonical bytes. The caller verifies (attest.VerifySignature) and inserts.
// Constraints: no controller identity parameter (R18h n3); the controller
// never signs on the node's behalf, it only signs the request.
// SPORT: internal.ci.RequestNodeSignature/ADDED (P1-CI-13).

import (
	"context"
	"encoding/base64"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RequestNodeSignature asks nodeID to sign its own run req.ExecutionID. sign
// is the installation key's signer (productionEnvelopeSigner's Sign bound to
// ctx in production); call sends one RPC to the node. A response whose
// canonical record does not carry this request's node, nonce, expiry,
// checkpoint, repo id, job spec hash and policy version is KindIntegrity.
func RequestNodeSignature(ctx context.Context, call func(ctx context.Context, method string, params, out any) error,
	sign func([]byte) ([]byte, error), nodeID string, req nodes.AttestSignRequest) (attest.SignedAttestation, error) {
	if call == nil || sign == nil || nodeID == "" {
		return attest.SignedAttestation{}, cascade.New(cascade.KindInvalidInput, "ci: RequestNodeSignature requires a call, a signer and a node id")
	}
	req.ControllerSig = ""
	payload, err := nodes.AttestSignPayload(nodeID, req)
	if err != nil {
		return attest.SignedAttestation{}, err
	}
	sig, err := sign(payload)
	if err != nil {
		return attest.SignedAttestation{}, err
	}
	req.ControllerSig = base64.StdEncoding.EncodeToString(sig)
	var out attest.SignedAttestation
	if err := call(ctx, nodes.AttestSignMethod, req, &out); err != nil {
		return attest.SignedAttestation{}, err
	}
	rec, err := attest.DecodeCanonical(out.CanonicalJSON)
	if err != nil {
		return attest.SignedAttestation{}, err
	}
	if !answersRequest(rec, nodeID, req) {
		return attest.SignedAttestation{}, cascade.New(cascade.KindIntegrity, "ci: the node's attestation does not answer this request")
	}
	out.Record = rec
	return out, nil
}

// answersRequest reports whether rec carries every identifier of req.
func answersRequest(rec attest.AttestationRecord, nodeID string, req nodes.AttestSignRequest) bool {
	return rec.NodeID == nodeID && rec.Nonce == req.Nonce && rec.ExpiresAt.Equal(req.ExpiresAt) &&
		rec.CheckpointID == req.CheckpointID && rec.RepoID == req.RepoID &&
		rec.JobSpecHash == req.JobSpecHash && rec.PolicyVersion == req.PolicyVersion &&
		rec.AttestorClass == attest.ClassEnrolledNode
}
