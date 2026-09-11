// Purpose: NodeKeystore, the R-16.44/R-21.220 custody seam for a node's
//
//	Ed25519 identity private key: generated once, stored in the OS
//	keychain/keyring/encrypted-file-vault (whichever internal/secrets
//	selects for this host), and never returned to a caller in plaintext
//	outside a Sign call.
//
// Inputs: an Identity's NodeID (the storage key) and, for Store, the raw
//
//	private key bytes fresh out of GenerateIdentity.
//
// Outputs: a signature over a caller-supplied payload, or a typed
//
//	fail-closed error.
//
// Constraints: R-21.220 — "identity private keys live in the OS keystore
//
//	(R-16.44 custody) — never in a config file, journal, payload, argv or
//	environment." internal/secrets.Custody (custody.go, R-16.44) is the
//	existing no-CGO OS-keystore primitive this tree already ships (macOS
//	Keychain via /usr/bin/security, linux secret-service via pure-Go
//	D-Bus, encrypted file-vault fallback); this file is a thin wrapper
//	over it rather than a second, competing keystore implementation.
//	Unlike internal/elevation's ElevationKeystore (which signs with an
//	auth-gated hardware key that never leaves its enclave),
//	NodeKeystore's private key is an ordinary Ed25519 key read from
//	custody and used in-process — there is no biometric gate here, only
//	custody-backend confidentiality at rest.
//
// SPORT: internal/nodes NodeKeystore/ADDED (P1-E17-W4-S36-T1).

package nodes

import (
	"context"
	"crypto/ed25519"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// keystoreService is the internal/secrets.Config.Service label node
// identity keys are filed under, distinct from every other custody
// consumer's label so a `vault list`-style enumeration never mixes node
// identity keys with unrelated secrets.
const keystoreService = "cascade-node-identity"

// keystoreSecretName returns the custody entry name for nodeID's private
// key. One entry per node id: a controller enrolling multiple worker
// identities (a future capability, not this ticket's) would key each
// separately.
func keystoreSecretName(nodeID string) string {
	return "identity-" + nodeID
}

// NodeKeystore is the custody seam for one node's Ed25519 identity private
// key. Every method fails closed (R-14.163): a backend that cannot prove
// a safe outcome returns a typed error, never a zero-value success.
type NodeKeystore struct {
	custody secrets.Custody
}

// NewNodeKeystore selects the custody backend for this host (the OS
// keychain/keyring when available, the encrypted file vault otherwise —
// secrets.SelectCustody's existing selection order) and returns a
// NodeKeystore over it. cfg is threaded through unchanged so tests can
// point Dir at a t.TempDir() and never touch a real OS keychain.
func NewNodeKeystore(cfg secrets.Config) (*NodeKeystore, error) {
	if cfg.Service == "" {
		cfg.Service = keystoreService
	}
	c, err := secrets.SelectCustody(cfg)
	if err != nil {
		return nil, err
	}
	return &NodeKeystore{custody: c}, nil
}

// Store persists priv under nodeID in the custody backend. It refuses a
// key of the wrong size rather than storing malformed material, and it is
// the ONLY place in this package a raw private key is ever written to
// durable storage — no code path in enroll.go, records.go, or transcript.go
// writes key bytes anywhere else (config file, journal, payload, argv, or
// environment), per R-21.220.
func (k *NodeKeystore) Store(ctx context.Context, nodeID string, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: private key has length %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	buf := append([]byte(nil), priv...)
	return k.custody.Set(ctx, keystoreSecretName(nodeID), buf)
}

// Sign loads nodeID's private key from custody and signs payload,
// returning the raw Ed25519 signature. The private key bytes are read
// into a local slice for the duration of this call and never returned to
// the caller. Returns cascade.ErrNotFound (via the custody backend's
// ErrSecretNotFound, which wraps KindNotFound) if no key is enrolled for
// nodeID.
func (k *NodeKeystore) Sign(ctx context.Context, nodeID string, payload []byte) ([]byte, error) {
	raw, err := k.custody.Get(ctx, keystoreSecretName(nodeID))
	if err != nil {
		return nil, err
	}
	defer zeroBytes(raw)
	if len(raw) != ed25519.PrivateKeySize {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"nodes: stored private key for %q has length %d, want %d (corrupt custody entry)", nodeID, len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	return ed25519.Sign(priv, payload), nil
}

// Delete removes nodeID's private key from custody (used by `node
// revoke`'s local-side cleanup; the revoked-key record itself is kept per
// rotate.go, only the signing capability is removed here).
func (k *NodeKeystore) Delete(ctx context.Context, nodeID string) error {
	return k.custody.Delete(ctx, keystoreSecretName(nodeID))
}

// BackendName reports which custody backend answered (os-keychain,
// secret-service, file-vault), for `cascade doctor` surfacing and tests.
func (k *NodeKeystore) BackendName() string {
	return k.custody.Name()
}

// zeroBytes overwrites b's contents so key material does not linger in
// memory beyond the call that needed it (mirrors internal/elevation's
// zero helper; duplicated rather than imported because internal/elevation
// must not become a dependency of internal/nodes for an unrelated
// four-line helper).
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
