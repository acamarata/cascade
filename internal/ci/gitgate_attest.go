package ci

// Purpose: AttestVerify (contract:receive-gate), the check the pre-receive
// gate runs on every protected ref's new tip.
//
// Inputs: the commit and tree the push wants, the ci_attestation store, the
// trust store, the controller's acceptance facts, the gate binding and the
// current policy version.
// Outputs: nil when one stored row is a verified, fresh, eligible, passing
// attestation that a controller acceptance fact binds to the same commit,
// tree, repository, checkpoint, job spec and policy version.
// Constraints: the gate only reads acceptance facts; it never consumes a
// nonce. The attestor class is derived from proof (attest.DeriveClass),
// never read from the signer's record.

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNoAttestation is returned when attestation is required and no
// ci_attestation row exists for the commit and tree.
var ErrNoAttestation = cascade.New(cascade.KindPolicyDenied, "ci: no attestation for this commit and tree")

// TrustStore resolves the public key a node signed an attestation with.
type TrustStore = attest.TrustStore

// AcceptanceLookup reads the acceptance fact the controller recorded for a
// nonce, commit, tree and checkpoint. It never consumes the nonce.
type AcceptanceLookup interface {
	AcceptedFor(ctx context.Context, nonce, commit, tree, checkpoint string) (jobs.AcceptanceFact, bool, error)
}

// GateBinding answers the registry repo id bound to a canonical gate dir.
type GateBinding interface {
	GateRepoID(ctx context.Context, gateDir string) (string, error)
}

// attestCheck carries the per-call inputs every row is checked against.
type attestCheck struct {
	want          AttestWant
	ts            TrustStore
	accepted      AcceptanceLookup
	currentPolicy func() (string, error)
	now           time.Time
}

// AttestVerify accepts want's tip only when one stored row passes
// VerifySignature for want.CommitSHA and want.TreeHash, derives an eligible
// class, is passing, and carries an AcceptedFor fact for the same commit,
// tree and checkpoint whose repo id, job spec hash and policy version match
// the record and want.RepoID. currentPolicy is nil on a node (the
// controller-signed fact's policy version is authoritative there); on the
// controller role gates must be non-nil and bind want.GateDir to want.RepoID.
// With require false and no rows, the tip is not gated and nil is returned.
func AttestVerify(ctx context.Context, want AttestWant, require bool, store *jobs.CiAttestationStore, ts TrustStore, accepted AcceptanceLookup, gates GateBinding, currentPolicy func() (string, error), now time.Time) error {
	if store == nil || ts == nil || accepted == nil || want.CommitSHA == "" || want.TreeHash == "" || want.RepoID == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: AttestVerify requires a store, trust, acceptance lookup, commit, tree and repo id")
	}
	if err := checkGateBinding(ctx, want, gates, currentPolicy); err != nil {
		return err
	}
	rows, err := store.ByCommitAndTree(ctx, want.CommitSHA, want.TreeHash)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		if require {
			return ErrNoAttestation
		}
		return nil
	}
	c := attestCheck{want: want, ts: ts, accepted: accepted, currentPolicy: currentPolicy, now: now}
	var first error
	for _, row := range rows {
		err := c.row(ctx, row)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// checkGateBinding refuses a controller-role call without a gate binding and
// a config repo id that disagrees with the registry binding.
func checkGateBinding(ctx context.Context, want AttestWant, gates GateBinding, currentPolicy func() (string, error)) error {
	if gates == nil {
		if currentPolicy != nil {
			return cascade.New(cascade.KindPolicyDenied, "ci: the controller role requires a gate binding")
		}
		return nil
	}
	bound, err := gates.GateRepoID(ctx, want.GateDir)
	if err != nil {
		return err
	}
	if bound != want.RepoID {
		return cascade.New(cascade.KindPolicyDenied, "ci: the gate's repo id disagrees with its registry binding")
	}
	return nil
}

// row checks one stored attestation row end to end.
func (c attestCheck) row(ctx context.Context, row jobs.CiAttestationRow) error {
	if err := row.CheckSigned(); err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrVerifyFailed, "ci: %v", err)
	}
	sa := attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature}
	if err := attest.VerifySignature(ctx, sa, c.want.CommitSHA, c.want.TreeHash, c.ts, c.now); err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrVerifyFailed, "ci: %v", err)
	}
	countersign, _ := c.ts.(attest.CountersignVerifier)
	if class := attest.DeriveClass(sa, c.ts, countersign); !class.Eligible() {
		return cascade.Wrapf(cascade.KindPolicyDenied, ErrUnattestedEvidence, "ci: derived attestor class %q", class)
	}
	rec, err := attest.DecodeCanonical(sa.CanonicalJSON)
	if err != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrVerifyFailed, "ci: %v", err)
	}
	fact, ok, err := c.accepted.AcceptedFor(ctx, rec.Nonce, c.want.CommitSHA, c.want.TreeHash, rec.CheckpointID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Wrap(cascade.KindPolicyDenied, ErrUnattestedEvidence, "ci: no acceptance fact for this commit, tree and checkpoint")
	}
	return c.fact(rec, fact)
}

// fact compares the acceptance fact against want and the signed record;
// the record is never compared against itself.
func (c attestCheck) fact(rec attest.AttestationRecord, fact jobs.AcceptanceFact) error {
	if fact.Commit != c.want.CommitSHA || fact.Tree != c.want.TreeHash || fact.RepoID != c.want.RepoID {
		return cascade.Wrap(cascade.KindPolicyDenied, ErrUnattestedEvidence, "ci: the acceptance fact does not bind the gated repository and tip")
	}
	if rec.RepoID != fact.RepoID || rec.CheckpointID != fact.CheckpointID ||
		rec.JobSpecHash != fact.JobSpecHash || rec.PolicyVersion != fact.PolicyVersion {
		return cascade.Wrap(cascade.KindPolicyDenied, ErrUnattestedEvidence, "ci: the acceptance fact does not bind this record")
	}
	if c.currentPolicy != nil {
		current, err := c.currentPolicy()
		if err != nil {
			return cascade.Wrap(cascade.KindPolicyDenied, err, "ci: read the current policy version")
		}
		if fact.PolicyVersion != current {
			return cascade.Wrap(cascade.KindPolicyDenied, ErrUnattestedEvidence, "ci: the acceptance fact's policy version is not current")
		}
	}
	if !rec.Passing(fact.PlanKinds) {
		return cascade.Wrap(cascade.KindPolicyDenied, ErrUnattestedEvidence, "ci: the attested run did not pass")
	}
	return nil
}
