package jobs

// Purpose: the attestation_nonce table and AttestationNonceStore
// (contract:ci-attestation, R18 M1): a nonce is issued for one acceptance
// re-run with the repo id, job spec hash, policy version and plan kinds the
// controller expects, consumed exactly once when the attested result is
// accepted, and read back as an AcceptanceFact by verifiers.
//
// Inputs: the controller's job, checkpoint and repo registry id at Issue;
// the accepting transaction, commit and tree at consume.
// Outputs: AcceptanceFact, the expected values a verifier compares a
// record against (never the record against itself).
// Constraints: 128 random bits per nonce; ttl capped at 24 h; a second
// consume is ErrNonceReused, an expired one ErrExpiredAttestation; Issued
// and AcceptedFor are reads and never consume.
//
// SPORT: internal.jobs.AttestationNonceStore/ADDED (P1-CI-04).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableAttestationNonce = "attestation_nonce"

// maxNonceTTL caps an attestation nonce's lifetime.
const maxNonceTTL = 24 * time.Hour

// AcceptanceFact is what the controller bound to a nonce: at Issue (job,
// checkpoint, repo id, job spec hash, policy version, plan kinds) and at
// acceptance (commit, tree, time).
type AcceptanceFact struct {
	Nonce, JobID, CheckpointID, RepoID, JobSpecHash, PolicyVersion, Commit, Tree string
	PlanKinds                                                                    []string
	AcceptedAt                                                                   time.Time
}

// AttestationNonceStore issues, consumes and reads attestation nonces.
type AttestationNonceStore struct {
	db    *sql.DB
	clock migrate.Clock
	rand  io.Reader
}

// NewAttestationNonceStore returns a store over db (jobs schema applied).
func NewAttestationNonceStore(db *sql.DB, clock migrate.Clock) (*AttestationNonceStore, error) {
	if db == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: NewAttestationNonceStore requires a db and a clock")
	}
	return &AttestationNonceStore{db: db, clock: clock, rand: rand.Reader}, nil
}

func attestationNonceTableStep() migrate.MigrationStep {
	text := func(name string, notNull bool) migrate.ColumnDef {
		return migrate.ColumnDef{Name: name, Type: migrate.TypeText, NotNull: notNull}
	}
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "attestation_nonce: one single-use nonce per acceptance re-run",
		Table: &migrate.TableDef{
			Name: tableAttestationNonce,
			Columns: []migrate.ColumnDef{
				{Name: "nonce", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				text("job_id", true), text("checkpoint_id", true), text("repo_id", true),
				text("job_spec_hash", true), text("policy_version", true), text("plan_kinds", true),
				{Name: "issued_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "expires_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "consumed_at", Type: migrate.TypeInteger},
				text("accepted_commit", false), text("accepted_tree", false),
			},
		},
	}
}

// Issue mints a nonce for one acceptance re-run, binding the values a
// verifier will expect. ttl must be positive and is capped at 24 h.
func (s *AttestationNonceStore) Issue(ctx context.Context, jobID, checkpointID, repoID, jobSpecHash, policyVersion string,
	planKinds []string, ttl time.Duration) (string, time.Time, error) {
	for _, v := range []string{jobID, checkpointID, repoID, jobSpecHash, policyVersion} {
		if v == "" {
			return "", time.Time{}, cascade.New(cascade.KindInvalidInput, "jobs: Issue needs job, checkpoint, repo id, job spec hash and policy version")
		}
	}
	if ttl <= 0 || len(planKinds) == 0 || slices.ContainsFunc(planKinds, func(k string) bool { return k == "" || strings.Contains(k, ":") }) {
		return "", time.Time{}, cascade.New(cascade.KindInvalidInput, "jobs: Issue needs a positive ttl and non-empty plan kinds without ':'")
	}
	var raw [16]byte
	if _, err := io.ReadFull(s.rand, raw[:]); err != nil {
		return "", time.Time{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read nonce entropy")
	}
	kinds := slices.Sorted(slices.Values(planKinds))
	encoded, _ := json.Marshal(kinds)
	now := s.clock.Now().UTC().Truncate(time.Second)
	expires := now.Add(min(ttl, maxNonceTTL)).Truncate(time.Second)
	nonce := hex.EncodeToString(raw[:])
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableAttestationNonce+` (nonce, job_id, checkpoint_id, repo_id,
		job_spec_hash, policy_version, plan_kinds, issued_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nonce, jobID, checkpointID, repoID, jobSpecHash, policyVersion, string(encoded), now.Unix(), expires.Unix())
	if err != nil {
		return "", time.Time{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: insert attestation_nonce")
	}
	return nonce, expires, nil
}

// ConsumeOnAcceptTx consumes nonce inside the accepting transaction tx,
// recording commit and tree. A consumed nonce is ErrNonceReused, an
// expired one ErrExpiredAttestation, another job's nonce KindInvalidInput.
func (s *AttestationNonceStore) ConsumeOnAcceptTx(ctx context.Context, tx *sql.Tx, nonce, jobID, commit, tree string, now time.Time) error {
	if tx == nil || nonce == "" || commit == "" || tree == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: ConsumeOnAcceptTx needs a transaction, nonce, commit and tree")
	}
	var boundJob string
	var expires int64
	var consumed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT job_id, expires_at, consumed_at FROM `+tableAttestationNonce+` WHERE nonce = ?`, nonce).
		Scan(&boundJob, &expires, &consumed)
	switch {
	case err == sql.ErrNoRows:
		return cascade.New(cascade.KindNotFound, "jobs: unknown attestation nonce")
	case err != nil:
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: read attestation_nonce")
	case consumed.Valid:
		return cascade.Wrap(cascade.KindConflict, attest.ErrNonceReused, "jobs: attestation nonce already consumed")
	case !now.Before(time.Unix(expires, 0)):
		return cascade.Wrap(cascade.KindPolicyDenied, attest.ErrExpiredAttestation, "jobs: attestation nonce expired")
	case boundJob != jobID:
		return cascade.New(cascade.KindInvalidInput, "jobs: attestation nonce was issued for another job")
	}
	res, err := tx.ExecContext(ctx, `UPDATE `+tableAttestationNonce+` SET consumed_at = ?, accepted_commit = ?, accepted_tree = ?
		WHERE nonce = ? AND consumed_at IS NULL`, now.Unix(), commit, tree, nonce)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: consume attestation_nonce")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return cascade.Wrap(cascade.KindConflict, attest.ErrNonceReused, "jobs: attestation nonce already consumed")
	}
	return nil
}

// Issued returns the fact as bound at Issue (no commit, tree or acceptance
// time); ok is false for an unknown nonce. It never consumes.
func (s *AttestationNonceStore) Issued(ctx context.Context, nonce string) (AcceptanceFact, bool, error) {
	f, _, ok, err := s.read(ctx, nonce)
	if err != nil || !ok {
		return AcceptanceFact{}, false, err
	}
	f.Commit, f.Tree, f.AcceptedAt = "", "", time.Time{}
	return f, true, nil
}

// AcceptedFor returns the fact of a consumed nonce whose acceptance bound
// exactly commit, tree and checkpoint; ok is false otherwise. It never
// consumes.
func (s *AttestationNonceStore) AcceptedFor(ctx context.Context, nonce, commit, tree, checkpoint string) (AcceptanceFact, bool, error) {
	f, consumed, ok, err := s.read(ctx, nonce)
	if err != nil || !ok || !consumed {
		return AcceptanceFact{}, false, err
	}
	if commit == "" || f.Commit != commit || f.Tree != tree || f.CheckpointID != checkpoint {
		return AcceptanceFact{}, false, nil
	}
	return f, true, nil
}

// read loads one nonce row; consumed reports whether it was accepted.
func (s *AttestationNonceStore) read(ctx context.Context, nonce string) (AcceptanceFact, bool, bool, error) {
	var f AcceptanceFact
	var kinds string
	var consumedAt sql.NullInt64
	var commit, tree sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT nonce, job_id, checkpoint_id, repo_id, job_spec_hash, policy_version,
		plan_kinds, consumed_at, accepted_commit, accepted_tree FROM `+tableAttestationNonce+` WHERE nonce = ?`, nonce).
		Scan(&f.Nonce, &f.JobID, &f.CheckpointID, &f.RepoID, &f.JobSpecHash, &f.PolicyVersion, &kinds, &consumedAt, &commit, &tree)
	if err == sql.ErrNoRows {
		return AcceptanceFact{}, false, false, nil
	}
	if err != nil {
		return AcceptanceFact{}, false, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read attestation_nonce")
	}
	if err := json.Unmarshal([]byte(kinds), &f.PlanKinds); err != nil {
		return AcceptanceFact{}, false, false, cascade.Wrap(cascade.KindIntegrity, err, "jobs: attestation_nonce plan_kinds is corrupt")
	}
	f.Commit, f.Tree = commit.String, tree.String
	if consumedAt.Valid {
		f.AcceptedAt = time.Unix(consumedAt.Int64, 0).UTC()
	}
	return f, consumedAt.Valid, true, nil
}
