// Purpose: build the node's signed enrollment payload and fingerprint its sshd key.
// Inputs: public enrollment fields and a custody-backed node keystore.
// Outputs: a signed payload or a typed refusal before signing invalid inputs.
// Constraints: private keys stay in custody; only ssh-ed25519 host keys are accepted.
// SPORT: internal/nodes node enrollment payload.

package nodes

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"golang.org/x/crypto/ssh"
)

// NodeEnrollPayload is the node-signed half of the enrollment handshake.
type NodeEnrollPayload struct {
	NodeID              string `json:"node_id"`
	NodePubKeyB64       string `json:"node_pubkey_b64"`
	NodeSignatureB64    string `json:"node_signature_b64"`
	ControllerPubKeyB64 string `json:"controller_pubkey_b64"`
	HostKeyFingerprint  string `json:"host_key_fingerprint"`
	SocketDir           string `json:"socket_dir"`
}

// BuildNodeEnrollPayload binds the node identity to the controller installation
// public key and the node's ssh-ed25519 host-key fingerprint. It signs only
// through NodeKeystore.Sign, after validating all supplied enrollment fields.
func BuildNodeEnrollPayload(ctx context.Context, ks *NodeKeystore, self Identity, controllerPubKeyB64, hostKeyFingerprint, socketDir string) (NodeEnrollPayload, error) {
	if _, err := ParsePublicKey(controllerPubKeyB64); err != nil {
		return NodeEnrollPayload{}, err
	}
	if len(hostKeyFingerprint) != 64 || strings.Trim(hostKeyFingerprint, "0123456789abcdef") != "" {
		return NodeEnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: host key fingerprint must be 64 lowercase hex characters")
	}
	if socketDir == "" {
		return NodeEnrollPayload{}, cascade.New(cascade.KindInvalidInput, "nodes: enroll payload socket_dir is required")
	}
	if err := validateSocketDir(socketDir); err != nil {
		return NodeEnrollPayload{}, err
	}
	tr := Transcript{NodeID: self.NodeID, NodePubKeyB64: self.PubKeyB64(),
		ControllerPubKeyB64: controllerPubKeyB64, HostKeyFingerprint: hostKeyFingerprint}
	sig, err := ks.Sign(ctx, self.NodeID, tr.SigningPayload())
	if err != nil {
		return NodeEnrollPayload{}, err
	}
	return NodeEnrollPayload{NodeID: tr.NodeID, NodePubKeyB64: tr.NodePubKeyB64,
		NodeSignatureB64: base64.StdEncoding.EncodeToString(sig), ControllerPubKeyB64: controllerPubKeyB64,
		HostKeyFingerprint: hostKeyFingerprint, SocketDir: socketDir}, nil
}

// HostKeyFingerprintFromFile reads an sshd authorized public key file, bounded
// to 16 KiB, and fingerprints its ssh-ed25519 wire encoding.
func HostKeyFingerprintFromFile(path string) (string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", cascade.Newf(cascade.KindNotFound, "nodes: host key %s not found", path)
	}
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "nodes: cannot open host key "+path)
	}
	defer func() { _ = f.Close() }()
	const maxHostKeyBytes = 16 * 1024
	raw, err := io.ReadAll(io.LimitReader(f, maxHostKeyBytes+1))
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "nodes: cannot read host key "+path)
	}
	if len(raw) > maxHostKeyBytes {
		return "", cascade.Newf(cascade.KindInvalidInput, "nodes: host key %s exceeds 16384 bytes", path)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return "", cascade.Newf(cascade.KindInvalidInput, "nodes: host key %s is not a valid authorized key", path)
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return "", cascade.Newf(cascade.KindInvalidInput, "nodes: host key %s is not ssh-ed25519", path)
	}
	return HostKeyFingerprint(key.Marshal()), nil
}
