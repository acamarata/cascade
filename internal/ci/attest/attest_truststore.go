package attest

// Purpose: the two TrustStore implementations (contract:ci-attestation):
// the node RecordStore (current, unrevoked key only) and the controller's
// own identity in front of it.
//
// Inputs: the controller's *nodes.RecordStore and its self Identity.
// Outputs: the public key of (node_id, key_id), or ErrUnknownNode /
// ErrRevokedKey.
// Constraints: a superseded key (nodes.RecordStore.Rotate supersedes at
// once, no overlap window) and a revoked current key both refuse; there is
// no second signing identity, the node identity key is the only key.
// SPORT: internal.ci.attest.NewRecordTrustStore/ADDED,
// internal.ci.attest.NewControllerTrustStore/ADDED (P1-CI-04).

import (
	"context"
	"crypto/ed25519"
	"errors"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordTrustStore resolves keys through the enrolled device records.
type recordTrustStore struct{ records *nodes.RecordStore }

// NewRecordTrustStore returns a TrustStore over rs: only the node's
// current, unrevoked key resolves; any other key id of a known node is
// ErrRevokedKey and an unknown node is ErrUnknownNode.
func NewRecordTrustStore(rs *nodes.RecordStore) TrustStore {
	return recordTrustStore{records: rs}
}

// Lookup implements TrustStore.
func (s recordTrustStore) Lookup(_ context.Context, nodeID, keyID string) (ed25519.PublicKey, error) {
	if s.records == nil || nodeID == "" {
		return nil, unknownNode()
	}
	rec, err := s.records.Get(nodeID)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return nil, unknownNode()
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "attest: read the node record")
	}
	pub, err := nodes.ParsePublicKey(rec.PubKeyB64)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "attest: the node record's key is corrupt")
	}
	if nodes.SignatureRevoked(rec, rec.PubKeyB64) || nodes.KeyIDFor(pub) != keyID {
		return nil, revokedKey()
	}
	if !nodes.Satisfies(rec.Tier, nodes.GateRestricted) {
		return nil, cascade.Wrap(cascade.KindCapabilityDenied, ErrUnverifiedAttestor, "attest: the node's trust tier may not attest CI runs")
	}
	return pub, nil
}

// controllerTrustStore answers for the controller's own identity and
// delegates every other node to next.
type controllerTrustStore struct {
	self nodes.Identity
	next TrustStore
}

// NewControllerTrustStore returns a TrustStore that resolves self's node id
// to self's key (any other key id of self is ErrRevokedKey) and every other
// node through next (nil next: ErrUnknownNode). When next also holds a
// record for self (the controller enrolled as a node), a rotation or
// revocation there refuses the self key too.
func NewControllerTrustStore(self nodes.Identity, next TrustStore) TrustStore {
	return controllerTrustStore{self: self, next: next}
}

// Lookup implements TrustStore.
func (s controllerTrustStore) Lookup(ctx context.Context, nodeID, keyID string) (ed25519.PublicKey, error) {
	if nodeID != "" && nodeID == s.self.NodeID && len(s.self.PubKey) == ed25519.PublicKeySize {
		if nodes.KeyIDFor(s.self.PubKey) != keyID {
			return nil, revokedKey()
		}
		if s.next != nil {
			if _, err := s.next.Lookup(ctx, nodeID, keyID); err != nil && !isSentinel(err, ErrUnknownNode) {
				return nil, err
			}
		}
		return s.self.PubKey, nil
	}
	if s.next == nil {
		return nil, unknownNode()
	}
	return s.next.Lookup(ctx, nodeID, keyID)
}

func unknownNode() error {
	return cascade.Wrap(cascade.KindNotFound, ErrUnknownNode, "attest: no trust store knows this node")
}

func revokedKey() error {
	return cascade.Wrap(cascade.KindPermissionDenied, ErrRevokedKey, "attest: key_id is not the node's current key")
}

// isSentinel reports whether target appears by identity on err's chain
// (cascade errors compare by Kind under errors.Is).
func isSentinel(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}
