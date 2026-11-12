package ci

// Purpose: resolve only exact node/key pairs vouched by verified delivery.
// Constraints: read failures refuse; no fallback to other trust sources.

import (
	"context"
	"crypto/ed25519"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

type vouchedTrustStore struct{ store *jobs.CiAttestTrustStore }

// NewVouchedTrustStore resolves keys exclusively from the node's vouches.
func NewVouchedTrustStore(store *jobs.CiAttestTrustStore) attest.TrustStore {
	return vouchedTrustStore{store: store}
}

// Lookup resolves an exact vouched pair and refuses corrupt or mismatched keys.
func (s vouchedTrustStore) Lookup(ctx context.Context, nodeID, keyID string) (ed25519.PublicKey, error) {
	unknown := func() error {
		return cascade.Wrap(cascade.KindNotFound, attest.ErrUnknownNode, "ci: no vouched node key")
	}
	if s.store == nil || nodeID == "" || keyID == "" {
		return nil, unknown()
	}
	b64, found, err := s.store.Lookup(ctx, nodeID, keyID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: read vouched key")
	}
	if !found {
		return nil, unknown()
	}
	pub, err := nodes.ParsePublicKey(b64)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "ci: corrupt vouched key")
	}
	if nodes.KeyIDFor(pub) != keyID {
		return nil, cascade.Wrap(cascade.KindPermissionDenied, attest.ErrRevokedKey, "ci: vouched key disagrees with key id")
	}
	return pub, nil
}
