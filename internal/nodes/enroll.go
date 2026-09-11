// Purpose: the enrollment/pairing protocol: the untrusted handshake
//
//	payload, its fail-closed decode/validate path (the FuzzEnrollHandshakePayload
//	target's subject), the EnrollNode operation that admits a node into
//	the fleet, and the JSON-RPC "node.enroll" handler that wires it to a
//	production caller.
//
// Inputs: EnrollPayload wire bytes from an untrusted peer (pre-trust —
//
//	the peer is not yet an enrolled node when this runs).
//
// Outputs: a persisted DeviceRecord, or a typed fail-closed error.
// Constraints: 06 §5.14 — "node enroll" is an unconditionally elevated
//
//	verb (internal/rpc's elevationTable already carries "node.enroll",
//	always: true — this ticket registers the handler internal/rpc
//	dispatches TO once elevation clears, it does not re-implement
//	elevation gating). D/S-07.T4 §D-24 — a daemonless (embedded) process
//	refuses an elevated verb unless the local daemonless-elevation
//	precondition (helper enrolled AND authenticator available) holds.
//	R-21.220 — no trust_tier is a typed error, never defaulted;
//	paired-device is not assignable here (ValidateTier's allowPaired=false
//	enforces it); re-enroll of a live identity is a typed conflict.
//
// SPORT: internal/nodes EnrollPayload/ADDED, EnrollNode/ADDED,
//
//	RegisterHandlers/ADDED (P1-E17-W4-S36-T1).

package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EnrollPayload is the untrusted wire shape of the enrollment handshake:
// the node's claimed identity, its requested trust tier, the ssh host
// this enrollment ran over plus the key it presented, and the node's half
// of the mutually signed transcript. Every field is decoded and shape-
// validated by DecodeEnrollHandshakePayload before EnrollNode ever sees a
// value it trusts; EnrollNode itself validates the semantics (tier
// membership, host pin, signatures).
type EnrollPayload struct {
	NodeID        string `json:"node_id"`
	NodePubKeyB64 string `json:"node_pubkey_b64"`
	TrustTier     string `json:"trust_tier"`
	Host          string `json:"host"`
	// HostKeyFingerprint is the fingerprint the transport actually
	// observed from the peer during the ssh handshake (S-36.T3's output),
	// carried through the payload so EnrollNode can check it against the
	// pinned/override value without importing net itself.
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	// HostKeyOverride is the operator-supplied --host-key-fingerprint
	// flag value (a sha256 hex string), or "" when not supplied.
	HostKeyOverride  string `json:"host_key_fingerprint_override,omitempty"`
	NodeSignatureB64 string `json:"node_signature_b64"`
}

// maxEnrollPayloadBytes bounds the decoder against a memory-exhaustion
// attack from an unauthenticated pre-trust peer.
const maxEnrollPayloadBytes = 64 * 1024

// DecodeEnrollHandshakePayload fail-closed-decodes and shape-validates raw
// attacker-facing pre-trust bytes into an EnrollPayload. It NEVER panics
// on any input (this is FuzzEnrollHandshakePayload's subject) and never
// returns a partially-valid payload: every required field is checked
// before the payload is handed back, so a caller that gets a nil error
// can trust the shape (not yet the semantics — trust_tier and the
// transcript signature still need EnrollNode's checks).
func DecodeEnrollHandshakePayload(raw []byte) (EnrollPayload, error) {
	if len(raw) == 0 {
		return EnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: enroll payload is empty")
	}
	if len(raw) > maxEnrollPayloadBytes {
		return EnrollPayload{}, cascade.Newf(cascade.KindInvalidInput,
			"nodes: enroll payload exceeds %d bytes", maxEnrollPayloadBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p EnrollPayload
	if err := dec.Decode(&p); err != nil {
		return EnrollPayload{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: enroll payload is not valid JSON")
	}
	if dec.More() {
		return EnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: enroll payload has trailing data after the JSON object")
	}
	if p.NodeID == "" || p.NodePubKeyB64 == "" {
		return EnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: enroll payload missing node_id or node_pubkey_b64")
	}
	if p.Host == "" || p.HostKeyFingerprint == "" {
		return EnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: enroll payload missing host or host_key_fingerprint")
	}
	if p.NodeSignatureB64 == "" {
		return EnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: enroll payload missing node_signature_b64")
	}
	// trust_tier is intentionally NOT defaulted here, not even to a
	// placeholder: an empty TrustTier is preserved verbatim so
	// ValidateTier's own fail-closed empty-string branch is the single
	// place that decision is made, never duplicated.
	return p, nil
}

// EnrollDeps carries every collaborator EnrollNode needs: the device
// record store, the known_hosts pinning store, the controller's own
// keystore (to produce ControllerSignature), and the controller's own
// enrolled identity — public half only; the private half never leaves
// Keystore.Sign.
type EnrollDeps struct {
	Records             *RecordStore
	KnownHosts          *KnownHosts
	Keystore            *NodeKeystore
	ControllerNodeID    string
	ControllerPubKeyB64 string
}

// EnrollNode runs the full enrollment operation: the daemonless-elevation
// precondition, host-key verification, trust_tier validation, transcript
// completion (the controller signs its half via the keystore) and
// verification, then a conflict-checked device-record write. Every
// failure mode is a typed fail-closed error; there is no path that
// returns a zero-value success.
func EnrollNode(ctx context.Context, deps EnrollDeps, precondition runtime.ElevationPrecondition, p EnrollPayload) (DeviceRecord, error) {
	if st, ok := runtime.DaemonlessStateFrom(ctx); ok && st.Embedded {
		helperEnrolled, authAvailable := runtime.DaemonlessElevationPrecondition(precondition)
		if !helperEnrolled || !authAvailable {
			return DeviceRecord{}, cascade.New(cascade.KindElevationRequired,
				"nodes: node enroll is an elevated verb; daemonless invocation refuses without an enrolled local helper and an available authenticator (D/S-07.T4 §D-24)")
		}
	}

	identity, err := ValidateIdentity(p.NodeID, p.NodePubKeyB64)
	if err != nil {
		return DeviceRecord{}, err
	}
	tier, err := ValidateTier(p.TrustTier, false)
	if err != nil {
		return DeviceRecord{}, err
	}
	if err := deps.KnownHosts.Verify(p.Host, p.HostKeyFingerprint, p.HostKeyOverride); err != nil {
		return DeviceRecord{}, err
	}

	nodeSig, err := decodeSignature(p.NodeSignatureB64)
	if err != nil {
		return DeviceRecord{}, err
	}
	tr := Transcript{
		NodeID:              identity.NodeID,
		NodePubKeyB64:       identity.PubKeyB64(),
		ControllerPubKeyB64: deps.ControllerPubKeyB64,
		HostKeyFingerprint:  p.HostKeyFingerprint,
		NodeSignature:       nodeSig,
	}
	ctrlSig, err := deps.Keystore.Sign(ctx, deps.ControllerNodeID, tr.SigningPayload())
	if err != nil {
		return DeviceRecord{}, err
	}
	tr.ControllerSignature = ctrlSig
	if err := VerifyTranscript(tr, p.HostKeyFingerprint); err != nil {
		return DeviceRecord{}, err
	}

	// A first-seen host is pinned as part of a successful enrollment
	// (Verify above already proved the presented fingerprint matches
	// either an existing pin or an explicit operator override); a
	// previously-pinned, matching host is a no-op re-pin.
	if err := deps.KnownHosts.Pin(p.Host, p.HostKeyFingerprint, false); err != nil {
		return DeviceRecord{}, err
	}

	return deps.Records.Enroll(identity, tier)
}

func decodeSignature(b64 string) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: node_signature_b64 is not valid base64")
	}
	return sig, nil
}

// RegisterHandlers mounts the "node.enroll" JSON-RPC method on registry,
// wiring EnrollNode to the daemon's real dispatch path. This IS the
// production caller for EnrollNode and DecodeEnrollHandshakePayload: the
// daemon's own composition root (cmd/cascade, out of this ticket's
// files_scope) calls RegisterHandlers once at startup; enroll_test.go
// drives the identical registry.Dispatch entry point a real client would
// use, and proves the wiring is load-bearing by showing Dispatch fails
// with method-not-found when RegisterHandlers is never called.
func RegisterHandlers(registry *rpc.Registry, deps EnrollDeps, precondition runtime.ElevationPrecondition) {
	registry.Register("node.enroll", func(ctx context.Context, params json.RawMessage) (any, error) {
		payload, err := DecodeEnrollHandshakePayload(params)
		if err != nil {
			return nil, err
		}
		return EnrollNode(ctx, deps, precondition, payload)
	})
}
