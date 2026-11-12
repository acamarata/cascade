package jobs

// Purpose: persist delivered acceptance facts and find their attestation rows.
// Constraints: delivered nonces are consumed, immutable and idempotent.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RecordAcceptance records a delivered, consumed acceptance fact. Repeating
// the same fact is a no-op; a different fact for its nonce is KindConflict.
func (s *AttestationNonceStore) RecordAcceptance(ctx context.Context, f AcceptanceFact) error {
	if err := validDeliveredAcceptance(f); err != nil {
		return err
	}
	f.PlanKinds = slices.Sorted(slices.Values(f.PlanKinds))
	f.AcceptedAt = time.Unix(f.AcceptedAt.Unix(), 0).UTC()
	kinds, _ := json.Marshal(f.PlanKinds)
	at := f.AcceptedAt.Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableAttestationNonce+`
		(nonce, job_id, checkpoint_id, repo_id, job_spec_hash, policy_version, plan_kinds,
		issued_at, expires_at, consumed_at, accepted_commit, accepted_tree)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		f.Nonce, f.JobID, f.CheckpointID, f.RepoID, f.JobSpecHash, f.PolicyVersion, string(kinds),
		at, at, at, f.Commit, f.Tree)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: record delivered acceptance")
	}
	got, consumed, ok, err := s.read(ctx, f.Nonce)
	if err != nil {
		return err
	}
	if !ok || !consumed || !sameDeliveredAcceptance(got, f) {
		return cascade.New(cascade.KindConflict, "jobs: nonce already binds a different acceptance")
	}
	return nil
}

func validDeliveredAcceptance(f AcceptanceFact) error {
	for _, v := range []string{f.Nonce, f.CheckpointID, f.RepoID, f.JobSpecHash, f.PolicyVersion, f.Commit, f.Tree} {
		if v == "" {
			return cascade.New(cascade.KindInvalidInput, "jobs: delivered acceptance is missing a binding")
		}
	}
	if f.AcceptedAt.IsZero() || len(f.PlanKinds) == 0 || slices.ContainsFunc(f.PlanKinds, func(k string) bool {
		return k == "" || strings.Contains(k, ":")
	}) {
		return cascade.New(cascade.KindInvalidInput, "jobs: delivered acceptance needs time and non-empty plan kinds without ':'")
	}
	return nil
}

func sameDeliveredAcceptance(a, b AcceptanceFact) bool {
	return a.Nonce == b.Nonce && a.JobID == b.JobID && a.CheckpointID == b.CheckpointID && a.RepoID == b.RepoID &&
		a.JobSpecHash == b.JobSpecHash && a.PolicyVersion == b.PolicyVersion && a.Commit == b.Commit && a.Tree == b.Tree &&
		a.AcceptedAt.Equal(b.AcceptedAt) && slices.Equal(a.PlanKinds, b.PlanKinds)
}

// ByNonce returns only the attestation rows bound to nonce, oldest first.
func (s *CiAttestationStore) ByNonce(ctx context.Context, nonce string) ([]CiAttestationRow, error) {
	return s.query(ctx, `nonce = ?`, nonce)
}
