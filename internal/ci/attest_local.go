package ci

// Purpose: bind completed local CI rows to a controller-issued acceptance fact.
// Inputs: custody signing, immutable run rows, nonce facts and controller trust.
// Outputs: a verified signed attestation and its evidence reference.
// Constraints: node-executed runs are never signed by the controller.
// SPORT: local acceptance attestation binding.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LocalAttestor signs completed local re-runs using controller custody.
type LocalAttestor struct {
	self   nodes.Identity
	sign   func([]byte) ([]byte, error)
	store  *jobs.CiAttestationStore
	trust  attest.TrustStore
	clock  runtime.Clock
	db     *sql.DB
	ledger *jobs.EvidenceLedger
}

// NewLocalAttestor binds the existing controller identity and its custody signer.
func NewLocalAttestor(self nodes.Identity, sign func([]byte) ([]byte, error), store *jobs.CiAttestationStore,
	ts attest.TrustStore, policyVersion func() string, clock runtime.Clock) (*LocalAttestor, error) {
	if self.NodeID == "" || len(self.PubKey) != 32 || sign == nil || store == nil || ts == nil || policyVersion == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "ci: local attestor requires identity, signer, store, trust, policy and clock")
	}
	return &LocalAttestor{self: self, sign: sign, store: store, trust: ts, clock: clock, db: store.DB()}, nil
}

// BindSources attaches the CI result database and the controller evidence ledger.
func (a *LocalAttestor) BindSources(db *sql.DB, ledger *jobs.EvidenceLedger) {
	a.db, a.ledger = db, ledger
}

// AcceptancePlan computes the same immutable plan the acceptance dispatcher uses.
func AcceptancePlan(ctx context.Context, ref JobRef, model RequirementModel, snap CandidateSnapshot) CIRequirementPlan {
	return acceptancePlan(ctx, ref, model, snap)
}

// AttestRerun verifies every local terminal row before signing its issued facts.
func (a *LocalAttestor) AttestRerun(ctx context.Context, res AcceptanceResult, ref JobRef, nonce string, expires time.Time) (string, error) {
	nonces, err := jobs.NewAttestationNonceStore(a.store.DB(), a.clock)
	if err != nil {
		return "", err
	}
	fact, ok, err := nonces.Issued(ctx, nonce)
	if err != nil {
		return "", err
	}
	if !ok || fact.JobID != ref.JobID || fact.CheckpointID != res.CheckpointID || len(res.Results) == 0 {
		return "", cascade.New(cascade.KindIntegrity, "ci: local attestation nonce or checkpoint mismatch")
	}
	if !rerunPlanMatches(res, fact) {
		return "", ErrCheckpointStale
	}
	rec := attest.AttestationRecord{SchemaVersion: attest.AttestationSchemaVersion,
		AttestorClass: attest.ClassEnrolledNode, CheckpointID: fact.CheckpointID, CommitSHA: ref.CheckpointCommit,
		TreeHash: res.TreeHash, RepoID: fact.RepoID, JobSpecHash: fact.JobSpecHash, PolicyVersion: fact.PolicyVersion,
		Nonce: nonce, ExpiresAt: expires, NodeID: a.self.NodeID, KeyID: nodes.KeyIDFor(a.self.PubKey),
		Environment:     attest.Environment{RunnerKind: "local", IsolationClass: IsolationCleanCheckoutSameUID, NodeID: a.self.NodeID},
		TargetSelection: strings.Join(fact.PlanKinds, ",")}
	if err := a.collect(ctx, &rec, res); err != nil {
		return "", err
	}
	sa, err := attest.Attest(rec, a.sign)
	if err != nil {
		return "", err
	}
	if err := attest.VerifySignature(ctx, sa, rec.CommitSHA, rec.TreeHash, a.trust, a.clock.Now()); err != nil {
		return "", err
	}
	row := localAttestationRow(sa, a.clock.Now())
	if err := a.store.Insert(ctx, row); err != nil {
		return "", err
	}
	if a.ledger != nil {
		if err := a.appendEvidence(ctx, row, ref); err != nil {
			return "", err
		}
	}
	return row.ID, nil
}

func rerunPlanMatches(res AcceptanceResult, fact jobs.AcceptanceFact) bool {
	if len(res.Results) != len(fact.PlanKinds) {
		return false
	}
	seen := make(map[string]bool, len(res.Results))
	for _, event := range res.Results {
		kind := string(event.Kind)
		if event.JobID != fact.JobID || seen[kind] {
			return false
		}
		seen[kind] = true
	}
	for _, kind := range fact.PlanKinds {
		if !seen[kind] {
			return false
		}
	}
	return true
}

func (a *LocalAttestor) collect(ctx context.Context, rec *attest.AttestationRecord, res AcceptanceResult) error {
	for _, event := range res.Results {
		if event.ExecutorKind != "local" || event.CheckpointID != res.CheckpointID || event.TreeHash != res.TreeHash {
			return cascade.New(cascade.KindPermissionDenied, "ci: controller signs only matching local executor results")
		}
		var commit, status, conclusion string
		var started, ended int64
		err := a.db.QueryRowContext(ctx, `SELECT head_sha,status,conclusion,created_at,updated_at FROM ci_run WHERE run_id=? AND repo_id=?`, event.RunID, event.RepoID).Scan(&commit, &status, &conclusion, &started, &ended)
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "ci: read attestation run")
		}
		if (commit != "" && commit != rec.CommitSHA) || status != "completed" {
			return cascade.New(cascade.KindIntegrity, "ci: attestation run is not terminal at checkpoint")
		}
		if commit == "" {
			var checkpoint string
			err := a.db.QueryRowContext(ctx, `SELECT checkpoint_id FROM ci_run_stream WHERE run_id=? AND repo_id=? AND via_stream=1`, event.RunID, event.RepoID).Scan(&checkpoint)
			if err != nil || checkpoint != rec.CheckpointID {
				return cascade.New(cascade.KindIntegrity, "ci: local run lacks its checkpoint binding")
			}
		}
		if conclusion != "success" {
			rec.ExitStatus = 1
		}
		var count, incomplete, failed int
		err = a.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN status!='completed' THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN conclusion!='success' THEN 1 ELSE 0 END),0) FROM ci_job WHERE run_id=?`, event.RunID).Scan(&count, &incomplete, &failed)
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "ci: read attestation jobs")
		}
		if count == 0 || incomplete > 0 {
			return cascade.New(cascade.KindIntegrity, "ci: attestation jobs are not terminal")
		}
		if failed > 0 {
			rec.ExitStatus = 1
		}
		start, end := time.UnixMilli(started), time.UnixMilli(ended)
		if rec.StartedAt.IsZero() || start.Before(rec.StartedAt) {
			rec.StartedAt = start
		}
		if end.After(rec.EndedAt) {
			rec.EndedAt = end
		}
		raw, _ := json.Marshal([]any{event.RunID, event.RepoID, commit, status, conclusion, started, ended, count, failed})
		hash := sha256.Sum256(raw)
		rec.ResultHashes = append(rec.ResultHashes, string(event.Kind)+":"+hex.EncodeToString(hash[:]))
	}
	return nil
}

func localAttestationRow(sa attest.SignedAttestation, now time.Time) jobs.CiAttestationRow {
	r := sa.Record
	return jobs.CiAttestationRow{ID: attest.RowID(sa), NodeID: r.NodeID, RepoID: r.RepoID, CommitSHA: r.CommitSHA,
		TreeHash: r.TreeHash, JobSpecHash: r.JobSpecHash, ToolchainFP: r.ToolchainFP, StartedAt: r.StartedAt.Unix(),
		EndedAt: r.EndedAt.Unix(), ExitStatus: r.ExitStatus, Signature: sa.Signature, CanonicalJSON: string(sa.CanonicalJSON),
		PolicyVersion: r.PolicyVersion, KeyID: r.KeyID, AttestorClass: string(attest.ClassEnrolledNode),
		CheckpointID: r.CheckpointID, TargetSelection: r.TargetSelection, Nonce: r.Nonce, ExpiresAt: r.ExpiresAt.Unix(), CreatedAt: now.Unix()}
}

func (a *LocalAttestor) appendEvidence(ctx context.Context, row jobs.CiAttestationRow, ref JobRef) error {
	var execution string
	err := a.store.DB().QueryRowContext(ctx, `SELECT id FROM jobs_execution WHERE job_id=? ORDER BY attempt DESC LIMIT 1`, ref.JobID).Scan(&execution)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: find attestation execution")
	}
	outcome := jobs.OutcomePass
	if row.ExitStatus != 0 {
		outcome = jobs.OutcomeFail
	}
	_, err = a.ledger.Append(ctx, jobs.EvidenceRecord{JobID: ref.JobID, Kind: jobs.EvidenceCIAttestation,
		ProducerCapability: jobs.ProducerAttestor, AttemptID: ref.AttemptID, CheckpointID: row.CheckpointID,
		TreeHash: row.TreeHash, AttestorIdentity: "node:" + row.NodeID, StartedAt: time.Unix(row.StartedAt, 0),
		EndedAt: time.Unix(row.EndedAt, 0), Outcome: outcome, ResultHash: row.ID, IdempotencyKey: fmt.Sprintf("ci_attestation:%s", row.ID),
		ArtifactRef: "ci_attestation:" + row.ID}, jobs.AppendAuthorization{ExecutionID: execution,
		LeaseRepoID: ref.Lease.RepoID, LeaseScopeGlob: ref.Lease.ScopeGlob, LeaseEpoch: ref.LeaseEpoch})
	return err
}
