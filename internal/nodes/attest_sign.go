// Purpose: the node side of ci.attest.sign (contract:node-attest-sign): the
//
//	strict request shape, the domain-separated payload the controller signs,
//	the check against the pinned controller key, and the handler that hands
//	a verified request to the composed AttestSigner.
//
// Inputs: an AttestSignRequest from the RPC caller; the pinned controller
//
//	key, read from the node's own ControllerBinding (never from the request).
//
// Outputs: the AttestSigner's signed attestation, or a typed refusal.
//
// Constraints: the request carries identifiers only. Result fields (exit
//
//	status, hashes, tree) are not part of the shape, and any field the shape
//	does not name refuses the whole request. Fields are joined with 0x1F
//	after a domain prefix (as transcript.go), so a field holding 0x1F
//	refuses. This file imports neither internal/ci nor internal/jobs.
//
// SPORT: internal/nodes AttestSignMethod/ADDED, AttestSignRequest/ADDED,
//
//	VerifyControllerRequest/ADDED, AttestSignPayload/ADDED (P1-CI-13).

package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AttestSignMethod is the node RPC method that signs one CI attestation.
const AttestSignMethod = "ci.attest.sign"

// attestSignDomain prefixes every controller-signed ci.attest.sign payload.
const attestSignDomain = "cascade.ci.attest.sign.v1"

// attestSignSep separates payload fields.
const attestSignSep = "\x1f"

// AttestSignRequest asks a node to sign the attestation of its own completed
// run ExecutionID. RepoID, JobSpecHash and PolicyVersion are copied by the
// controller from the issued nonce fact; the node copies them into the record
// it builds and never computes them. ControllerSig is standard base64 of the
// controller installation key's Ed25519 signature over AttestSignPayload.
type AttestSignRequest struct {
	ExecutionID   string    `json:"execution_id"`
	Nonce         string    `json:"nonce"`
	ExpiresAt     time.Time `json:"expires_at"`
	CheckpointID  string    `json:"checkpoint_id"`
	RepoID        string    `json:"repo_id"`
	JobSpecHash   string    `json:"job_spec_hash"`
	PolicyVersion string    `json:"policy_version"`
	ControllerSig string    `json:"controller_sig"`
}

// AttestSigner builds the node's own record for a verified request and signs
// it with the node identity key, returning the encoded signed attestation.
type AttestSigner func(ctx context.Context, req AttestSignRequest) (json.RawMessage, error)

// AttestSignPayload returns the bytes the controller signs for req addressed
// to nodeID: the domain prefix, then node id, execution id, nonce, expires_at
// (RFC 3339, UTC), checkpoint id, repo id, job spec hash and policy version,
// joined by 0x1F. An empty field, a field containing 0x1F, or an expires_at
// with a sub-second part is KindInvalidInput, so distinct requests never
// share a payload.
func AttestSignPayload(nodeID string, req AttestSignRequest) ([]byte, error) {
	if req.ExpiresAt.IsZero() || req.ExpiresAt.Nanosecond() != 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: ci.attest.sign expires_at must be a whole-second time")
	}
	fields := []string{nodeID, req.ExecutionID, req.Nonce, req.ExpiresAt.UTC().Format(time.RFC3339),
		req.CheckpointID, req.RepoID, req.JobSpecHash, req.PolicyVersion}
	for _, f := range fields {
		if f == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "nodes: ci.attest.sign request is missing a field")
		}
		if strings.Contains(f, attestSignSep) {
			return nil, cascade.New(cascade.KindInvalidInput, "nodes: ci.attest.sign field contains the 0x1F separator")
		}
	}
	return []byte(attestSignDomain + attestSignSep + strings.Join(fields, attestSignSep)), nil
}

// VerifyControllerRequest checks sig over payload against the controller key
// b pins. An empty or malformed pin, or a signature that does not verify, is
// KindPermissionDenied.
func VerifyControllerRequest(b ControllerBinding, payload, sig []byte) error {
	if b.ControllerPubKeyB64 == "" {
		return cascade.New(cascade.KindPermissionDenied, "nodes: no controller key is pinned on this node; run `cascade node bind` first")
	}
	pub, err := ParsePublicKey(b.ControllerPubKeyB64)
	if err != nil {
		return cascade.Wrap(cascade.KindPermissionDenied, err, "nodes: the pinned controller key is unusable")
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, payload, sig) {
		return cascade.New(cascade.KindPermissionDenied, "nodes: ci.attest.sign is not signed by the pinned controller key")
	}
	return nil
}

// attestSignDeps is what the ci.attest.sign handler needs.
type attestSignDeps struct {
	binding ControllerBindingBackend
	clock   runtime.Clock
	nodeID  string
	signer  AttestSigner
}

// attestSignHandler wraps serveAttestSign as an rpc.HandlerFunc.
func attestSignHandler(d attestSignDeps) rpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		return serveAttestSign(ctx, d, params)
	}
}

// serveAttestSign decodes strictly, refuses an expired request, verifies the
// controller signature against the node's own pin, and only then calls the
// signer.
func serveAttestSign(ctx context.Context, d attestSignDeps, params json.RawMessage) (json.RawMessage, error) {
	req, err := decodeAttestSignRequest(params)
	if err != nil {
		return nil, err
	}
	if d.clock == nil || d.signer == nil || d.nodeID == "" {
		return nil, cascade.New(cascade.KindPermissionDenied, "nodes: ci.attest.sign is not configured on this node")
	}
	if !req.ExpiresAt.After(d.clock.Now()) {
		return nil, cascade.New(cascade.KindPermissionDenied, "nodes: ci.attest.sign request has expired")
	}
	payload, err := AttestSignPayload(d.nodeID, req)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(req.ControllerSig)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindPermissionDenied, err, "nodes: ci.attest.sign controller signature is not base64")
	}
	binding, err := loadPin(d.binding)
	if err != nil {
		return nil, err
	}
	if err := VerifyControllerRequest(binding, payload, sig); err != nil {
		return nil, err
	}
	return d.signer(ctx, req)
}

// loadPin reads the binding from the node's own store; no store or no
// binding is an empty pin, which VerifyControllerRequest refuses.
func loadPin(backend ControllerBindingBackend) (ControllerBinding, error) {
	if backend == nil {
		return ControllerBinding{}, nil
	}
	binding, ok, err := backend.Load()
	if err != nil {
		return ControllerBinding{}, cascade.Wrap(cascade.KindPermissionDenied, err, "nodes: the controller binding cannot be read")
	}
	if !ok {
		return ControllerBinding{}, nil
	}
	return binding, nil
}

// decodeAttestSignRequest decodes exactly one AttestSignRequest object; an
// unknown field (a caller-supplied result among them) or trailing data is
// KindInvalidInput.
func decodeAttestSignRequest(params json.RawMessage) (AttestSignRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	var req AttestSignRequest
	if err := dec.Decode(&req); err != nil {
		return AttestSignRequest{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: ci.attest.sign params are not an attest-sign request")
	}
	if dec.More() {
		return AttestSignRequest{}, cascade.New(cascade.KindInvalidInput, "nodes: ci.attest.sign params carry trailing data")
	}
	return req, nil
}
