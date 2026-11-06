// Purpose: the node side of ci.attest.put: the strict contract params, the
//
//	domain-separated canonical bytes the controller signs, and the handler
//	that hands a verified request to the composed AttestPutter.
//
// Inputs: an AttestPutRequest from the RPC caller; the pinned controller
//
//	key, read from the node's own ControllerBinding (never from the request).
//
// Outputs: AttestPutResult{Stored: true}, or a typed refusal.
//
// Constraints: the signed bytes are the domain prefix, 0x1F, then the JSON
//
//	of a fixed-order struct over every field but controller_sig. No node id,
//	no clock and no expiry. Any unknown field refuses the whole request, and
//	the putter runs only after VerifyControllerRequest returned nil. This
//	file imports neither internal/ci nor internal/jobs.
//
// SPORT: internal/nodes AttestPutMethod/ADDED, AttestPutAcceptance/ADDED,
//
//	AttestPutRequest/ADDED, AttestPutResult/ADDED, AttestPutter/ADDED,
//	AttestPutPayload/ADDED (P1-CI-15d).

package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AttestPutMethod is the node RPC method that stores one accepted CI
// attestation.
const AttestPutMethod = "ci.attest.put"

// attestPutDomain prefixes every controller-signed ci.attest.put payload.
const attestPutDomain = "cascade.ci.attest.put.v1"

// AttestPutAcceptance is the controller's acceptance of one signed
// attestation: what it checked and when it accepted it.
type AttestPutAcceptance struct {
	CommitSHA     string    `json:"commit_sha"`
	TreeHash      string    `json:"tree_hash"`
	CheckpointID  string    `json:"checkpoint_id"`
	Nonce         string    `json:"nonce"`
	RepoID        string    `json:"repo_id"`
	JobSpecHash   string    `json:"job_spec_hash"`
	PolicyVersion string    `json:"policy_version"`
	PlanKinds     []string  `json:"plan_kinds"`
	AcceptedAt    time.Time `json:"accepted_at"`
}

// AttestPutRequest asks a node to store a signed attestation the controller
// accepted. ControllerSig is standard base64 of the controller installation
// key's Ed25519 signature over AttestPutPayload.
type AttestPutRequest struct {
	SignedAttestation json.RawMessage     `json:"signed_attestation"`
	SignerPubKeyB64   string              `json:"signer_pub_key_b64"`
	Acceptance        AttestPutAcceptance `json:"acceptance"`
	ControllerSig     string              `json:"controller_sig"`
}

// AttestPutResult is the ci.attest.put result.
type AttestPutResult struct {
	Stored bool `json:"stored"`
}

// AttestPutter stores one verified ci.attest.put request on the node.
type AttestPutter func(ctx context.Context, put AttestPutRequest) error

// attestPutCanonAcceptance fixes the signed key order of the acceptance.
type attestPutCanonAcceptance struct {
	AcceptedAt    string   `json:"accepted_at"`
	CheckpointID  string   `json:"checkpoint_id"`
	CommitSHA     string   `json:"commit_sha"`
	JobSpecHash   string   `json:"job_spec_hash"`
	Nonce         string   `json:"nonce"`
	PlanKinds     []string `json:"plan_kinds"`
	PolicyVersion string   `json:"policy_version"`
	RepoID        string   `json:"repo_id"`
	TreeHash      string   `json:"tree_hash"`
}

// attestPutCanon fixes the signed key order of the request.
type attestPutCanon struct {
	Acceptance        attestPutCanonAcceptance `json:"acceptance"`
	SignedAttestation json.RawMessage          `json:"signed_attestation"`
	SignerPubKeyB64   string                   `json:"signer_pub_key_b64"`
}

// AttestPutPayload returns the bytes the controller signs for req: the
// domain prefix, 0x1F, then the canonical JSON of every field except
// ControllerSig (accepted_at as UTC RFC 3339, nil plan_kinds as [], the
// signed attestation compacted). An empty acceptance string or plan kind, a
// zero or sub-second accepted_at, an empty signer key, or a signed
// attestation that is not a JSON object is KindInvalidInput.
func AttestPutPayload(req AttestPutRequest) ([]byte, error) {
	a := req.Acceptance
	if a.AcceptedAt.IsZero() || a.AcceptedAt.Nanosecond() != 0 {
		return nil, invalidPut("accepted_at must be a whole-second time")
	}
	for _, f := range []string{a.CommitSHA, a.TreeHash, a.CheckpointID, a.Nonce, a.RepoID, a.JobSpecHash, a.PolicyVersion, req.SignerPubKeyB64} {
		if f == "" {
			return nil, invalidPut("request is missing a field")
		}
	}
	kinds := make([]string, 0, len(a.PlanKinds))
	for _, k := range a.PlanKinds {
		if k == "" {
			return nil, invalidPut("plan_kinds holds an empty kind")
		}
		kinds = append(kinds, k)
	}
	att := bytes.TrimSpace(req.SignedAttestation)
	if len(att) == 0 || att[0] != '{' || !json.Valid(att) {
		return nil, invalidPut("signed_attestation is not a JSON object")
	}
	body, err := json.Marshal(attestPutCanon{
		Acceptance: attestPutCanonAcceptance{AcceptedAt: a.AcceptedAt.UTC().Format(time.RFC3339),
			CheckpointID: a.CheckpointID, CommitSHA: a.CommitSHA, JobSpecHash: a.JobSpecHash, Nonce: a.Nonce,
			PlanKinds: kinds, PolicyVersion: a.PolicyVersion, RepoID: a.RepoID, TreeHash: a.TreeHash},
		SignedAttestation: json.RawMessage(att),
		SignerPubKeyB64:   req.SignerPubKeyB64,
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: ci.attest.put request cannot be encoded")
	}
	return append([]byte(attestPutDomain+attestSignSep), body...), nil
}

// invalidPut is a KindInvalidInput refusal of a ci.attest.put request.
func invalidPut(msg string) error {
	return cascade.New(cascade.KindInvalidInput, "nodes: ci.attest.put "+msg)
}

// attestPutDeps is what the ci.attest.put handler needs.
type attestPutDeps struct {
	binding ControllerBindingBackend
	putter  AttestPutter
}

// attestPutHandler wraps serveAttestPut as an rpc.HandlerFunc.
func attestPutHandler(d attestPutDeps) rpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		return serveAttestPut(ctx, d, params)
	}
}

// serveAttestPut decodes strictly, builds the signed bytes, verifies the
// controller signature against the node's own pin, and only then stores.
func serveAttestPut(ctx context.Context, d attestPutDeps, params json.RawMessage) (AttestPutResult, error) {
	req, err := decodeAttestPutRequest(params)
	if err != nil {
		return AttestPutResult{}, err
	}
	payload, err := AttestPutPayload(req)
	if err != nil {
		return AttestPutResult{}, err
	}
	sig, err := base64.StdEncoding.DecodeString(req.ControllerSig)
	if err != nil {
		return AttestPutResult{}, cascade.Wrap(cascade.KindPermissionDenied, err, "nodes: ci.attest.put controller signature is not base64")
	}
	binding, err := loadPin(d.binding)
	if err != nil {
		return AttestPutResult{}, err
	}
	if err := VerifyControllerRequest(binding, payload, sig); err != nil {
		return AttestPutResult{}, err
	}
	if d.putter == nil {
		return AttestPutResult{}, cascade.New(cascade.KindPermissionDenied, "nodes: ci.attest.put is not configured on this node")
	}
	if err := d.putter(ctx, req); err != nil {
		return AttestPutResult{}, err
	}
	return AttestPutResult{Stored: true}, nil
}

// decodeAttestPutRequest decodes exactly one AttestPutRequest object; an
// unknown field at any level or trailing data is KindInvalidInput.
func decodeAttestPutRequest(params json.RawMessage) (AttestPutRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	var req AttestPutRequest
	if err := dec.Decode(&req); err != nil {
		return AttestPutRequest{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: ci.attest.put params are not an attest-put request")
	}
	if dec.More() {
		return AttestPutRequest{}, invalidPut("params carry trailing data")
	}
	return req, nil
}
