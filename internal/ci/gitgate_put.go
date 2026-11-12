package ci

// Purpose: verify controller-delivered attestations before storing node evidence.
// Constraints: canonical bytes are authoritative; the row is written last.

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// NodeAttestPutter stores verified deliveries on the receiving node.
type NodeAttestPutter struct {
	rows   *jobs.CiAttestationStore
	nonces *jobs.AttestationNonceStore
	trust  *jobs.CiAttestTrustStore
	clock  runtime.Clock
}

// NewNodeAttestPutter binds delivery stores over an already migrated node db.
func NewNodeAttestPutter(db *sql.DB, clock runtime.Clock) (*NodeAttestPutter, error) {
	if db == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "ci: attestation putter requires db and clock")
	}
	rows, err := jobs.NewCiAttestationStore(db)
	if err != nil {
		return nil, err
	}
	nonces, err := jobs.NewAttestationNonceStore(db, clock)
	if err != nil {
		return nil, err
	}
	trust, err := jobs.NewCiAttestTrustStore(db)
	if err != nil {
		return nil, err
	}
	return &NodeAttestPutter{rows: rows, nonces: nonces, trust: trust, clock: clock}, nil
}

// NewAttestPutter returns the delivery handler over this attestor's node store.
func (a *NodeAttestor) NewAttestPutter() (*NodeAttestPutter, error) {
	return NewNodeAttestPutter(a.db, a.clock)
}

// Put rechecks a delivery after the node registry verifies its controller
// signature. It vouches the signer, stores acceptance, then appends the row.
func (p *NodeAttestPutter) Put(ctx context.Context, req nodes.AttestPutRequest) error {
	sa, ts, err := checkedPut(req)
	if err != nil {
		return err
	}
	now := p.clock.Now()
	rec := sa.Record
	if err := attest.VerifySignature(ctx, sa, rec.CommitSHA, rec.TreeHash, ts, now); err != nil {
		return err
	}
	if attest.DeriveClass(sa, ts, nil) != attest.ClassEnrolledNode {
		return cascade.New(cascade.KindPermissionDenied, "ci: delivered attestation is not an enrolled node")
	}
	row := localAttestationRow(sa, now)
	if err := row.CheckSigned(); err != nil {
		return err
	}
	if err := p.trust.Vouch(ctx, rec.NodeID, rec.KeyID, req.SignerPubKeyB64, now); err != nil {
		return err
	}
	a := req.Acceptance
	f := jobs.AcceptanceFact{Nonce: a.Nonce, CheckpointID: a.CheckpointID, RepoID: a.RepoID,
		JobSpecHash: a.JobSpecHash, PolicyVersion: a.PolicyVersion, Commit: a.CommitSHA, Tree: a.TreeHash,
		PlanKinds: a.PlanKinds, AcceptedAt: a.AcceptedAt}
	if err := p.nonces.RecordAcceptance(ctx, f); err != nil {
		return err
	}
	err = p.rows.Insert(ctx, row)
	if !errors.Is(err, jobs.ErrAlreadyAttested) {
		return err
	}
	stored, ok, readErr := p.rows.ByID(ctx, row.ID)
	if readErr != nil {
		return readErr
	}
	row.CreatedAt = stored.CreatedAt
	if ok && row == stored {
		return nil
	}
	return err
}

func checkedPut(req nodes.AttestPutRequest) (attest.SignedAttestation, putKey, error) {
	var sa attest.SignedAttestation
	if err := json.Unmarshal(req.SignedAttestation, &sa); err != nil {
		return sa, putKey{}, cascade.Wrap(cascade.KindInvalidInput, err, "ci: decode delivered attestation")
	}
	rec, err := attest.DecodeCanonical(sa.CanonicalJSON)
	if err != nil {
		return sa, putKey{}, err
	}
	sa.Record = rec
	pub, err := nodes.ParsePublicKey(req.SignerPubKeyB64)
	if err != nil {
		return sa, putKey{}, err
	}
	if nodes.KeyIDFor(pub) != rec.KeyID {
		return sa, putKey{}, cascade.Wrap(cascade.KindPermissionDenied, attest.ErrRevokedKey, "ci: delivered signer differs from key id")
	}
	a := req.Acceptance
	if rec.CommitSHA != a.CommitSHA || rec.TreeHash != a.TreeHash || rec.CheckpointID != a.CheckpointID ||
		rec.Nonce != a.Nonce || rec.RepoID != a.RepoID || rec.JobSpecHash != a.JobSpecHash || rec.PolicyVersion != a.PolicyVersion {
		return sa, putKey{}, cascade.New(cascade.KindIntegrity, "ci: delivered acceptance differs from signed record")
	}
	if _, err := nodes.AttestPutPayload(req); err != nil {
		return sa, putKey{}, err
	}
	if len(a.PlanKinds) == 0 {
		return sa, putKey{}, cascade.New(cascade.KindInvalidInput, "ci: acceptance needs plan kinds")
	}
	for _, kind := range a.PlanKinds {
		if strings.Contains(kind, ":") {
			return sa, putKey{}, cascade.New(cascade.KindInvalidInput, "ci: acceptance kind contains ':'")
		}
	}
	return sa, putKey{nodeID: rec.NodeID, keyID: rec.KeyID, pub: pub}, nil
}

type putKey struct {
	nodeID, keyID string
	pub           ed25519.PublicKey
}

// Lookup restricts verification to the signer pair decoded from canonical bytes.
func (k putKey) Lookup(_ context.Context, nodeID, keyID string) (ed25519.PublicKey, error) {
	if nodeID != k.nodeID || keyID != k.keyID {
		return nil, cascade.Wrap(cascade.KindNotFound, attest.ErrUnknownNode, "ci: unknown delivery signer")
	}
	return k.pub, nil
}
