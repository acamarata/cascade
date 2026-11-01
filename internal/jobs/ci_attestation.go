package jobs

// Purpose: the ci_attestation table and CiAttestationStore
// (contract:ci-attestation): one row per accepted signed CI attestation,
// keyed by attest.RowID (hex SHA-256 of the canonical record bytes).
//
// Inputs: a CiAttestationRow built from a verified attest.SignedAttestation.
// Outputs: rows by id, by commit, and by commit and tree.
// Constraints: append-only. A second insert of the same row id, or of the
// same (node_id, commit_sha, tree_hash, checkpoint_id), is
// ErrAlreadyAttested and leaves the stored row unchanged. Insert refuses a
// row whose id is not the digest of its canonical_json or whose columns
// disagree with the signed record. internal/jobs never imports internal/ci;
// the leaf internal/ci/attest is allowed.
//
// SPORT: internal.jobs.CiAttestationStore/ADDED (P1-CI-04).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableCiAttestation = "ci_attestation"

const ciAttestationColumns = `id, node_id, repo_id, commit_sha, tree_hash, job_spec_hash, toolchain_fp,
	started_at, ended_at, exit_status, signature, canonical_json, policy_version, key_id, attestor_class,
	checkpoint_id, target_selection, nonce, expires_at, accepted_before_revocation, created_at`

// ErrAlreadyAttested reports a ci_attestation row that already exists.
var ErrAlreadyAttested = cascade.New(cascade.KindConflict, "jobs: this attestation is already stored")

// CiAttestationRow is one ci_attestation row, one field per column. Times
// are unix seconds. AttestorClass is the class the controller derived
// (attest.DeriveClass), never the signer's assertion.
type CiAttestationRow struct {
	ID, NodeID, RepoID, CommitSHA, TreeHash, JobSpecHash, ToolchainFP string
	StartedAt, EndedAt                                                int64
	ExitStatus                                                        int
	Signature, CanonicalJSON, PolicyVersion, KeyID, AttestorClass     string
	CheckpointID, TargetSelection, Nonce                              string
	ExpiresAt                                                         int64
	AcceptedBeforeRevocation                                          bool
	CreatedAt                                                         int64
}

// CiAttestationStore reads and appends ci_attestation rows.
type CiAttestationStore struct{ db *sql.DB }

// NewCiAttestationStore returns a store over db, which must carry the jobs
// schema (ApplyJobsSchema).
func NewCiAttestationStore(db *sql.DB) (*CiAttestationStore, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: NewCiAttestationStore requires a non-nil db")
	}
	return &CiAttestationStore{db: db}, nil
}

func ciAttestationTableStep() migrate.MigrationStep {
	text := func(name string) migrate.ColumnDef {
		return migrate.ColumnDef{Name: name, Type: migrate.TypeText, NotNull: true}
	}
	integer := func(name string) migrate.ColumnDef {
		return migrate.ColumnDef{Name: name, Type: migrate.TypeInteger, NotNull: true}
	}
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_attestation: one row per accepted signed CI attestation",
		Table: &migrate.TableDef{
			Name: tableCiAttestation,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				text("node_id"), text("repo_id"), text("commit_sha"), text("tree_hash"), text("job_spec_hash"),
				text("toolchain_fp"), integer("started_at"), integer("ended_at"), integer("exit_status"),
				text("signature"), text("canonical_json"), text("policy_version"), text("key_id"),
				text("attestor_class"), text("checkpoint_id"), text("target_selection"), text("nonce"),
				integer("expires_at"), integer("accepted_before_revocation"), integer("created_at"),
			},
		},
	}
}

func ciAttestationUniqueStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateIndex,
		Description: "ci_attestation: one row per (node, commit, tree, checkpoint)",
		Index: &migrate.IndexDef{
			Name: "ci_attestation_node_commit_tree_checkpoint", Table: tableCiAttestation, Unique: true,
			Columns: []string{"node_id", "commit_sha", "tree_hash", "checkpoint_id"},
		},
	}
}

// Insert appends row. A duplicate id or (node, commit, tree, checkpoint)
// is ErrAlreadyAttested and changes nothing.
func (s *CiAttestationStore) Insert(ctx context.Context, row CiAttestationRow) error {
	if err := row.CheckSigned(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO `+tableCiAttestation+` (`+ciAttestationColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		row.ID, row.NodeID, row.RepoID, row.CommitSHA, row.TreeHash, row.JobSpecHash, row.ToolchainFP,
		row.StartedAt, row.EndedAt, row.ExitStatus, row.Signature, row.CanonicalJSON, row.PolicyVersion,
		row.KeyID, row.AttestorClass, row.CheckpointID, row.TargetSelection, row.Nonce, row.ExpiresAt,
		boolInt(row.AcceptedBeforeRevocation), row.CreatedAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: insert ci_attestation")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return cascade.Wrapf(cascade.KindConflict, ErrAlreadyAttested, "jobs: ci_attestation %s already stored", row.ID)
	}
	return nil
}

// CheckSigned refuses a row whose id is not attest.RowID of its canonical
// bytes, whose columns are not the signed record's values, or whose
// attestor_class is not a class name. It checks no signature: Insert runs it
// before storing and a verifier runs it on every row it reads.
func (row CiAttestationRow) CheckSigned() error {
	if row.ID != attest.RowID(attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON)}) {
		return cascade.New(cascade.KindIntegrity, "jobs: ci_attestation id is not the digest of its canonical_json")
	}
	if string(attest.ParseAttestorClass(row.AttestorClass)) != row.AttestorClass {
		return cascade.New(cascade.KindInvalidInput, "jobs: ci_attestation attestor_class is not a class name")
	}
	rec, err := attest.DecodeCanonical([]byte(row.CanonicalJSON))
	if err != nil {
		return err
	}
	want := CiAttestationRow{
		ID: row.ID, NodeID: rec.NodeID, RepoID: rec.RepoID, CommitSHA: rec.CommitSHA, TreeHash: rec.TreeHash,
		JobSpecHash: rec.JobSpecHash, ToolchainFP: rec.ToolchainFP, StartedAt: rec.StartedAt.Unix(),
		EndedAt: rec.EndedAt.Unix(), ExitStatus: rec.ExitStatus, Signature: row.Signature,
		CanonicalJSON: row.CanonicalJSON, PolicyVersion: rec.PolicyVersion, KeyID: rec.KeyID,
		AttestorClass: row.AttestorClass, CheckpointID: rec.CheckpointID, TargetSelection: rec.TargetSelection,
		Nonce: rec.Nonce, ExpiresAt: rec.ExpiresAt.Unix(), AcceptedBeforeRevocation: row.AcceptedBeforeRevocation,
		CreatedAt: row.CreatedAt,
	}
	if row != want {
		return cascade.New(cascade.KindIntegrity, "jobs: ci_attestation columns disagree with the signed record")
	}
	return nil
}

// ByID returns the row with id; ok is false when none exists.
func (s *CiAttestationStore) ByID(ctx context.Context, id string) (CiAttestationRow, bool, error) {
	rows, err := s.query(ctx, `id = ?`, id)
	if err != nil || len(rows) == 0 {
		return CiAttestationRow{}, false, err
	}
	return rows[0], true, nil
}

// ByCommitSHA returns every row for commitSHA, oldest first.
func (s *CiAttestationStore) ByCommitSHA(ctx context.Context, commitSHA string) ([]CiAttestationRow, error) {
	return s.query(ctx, `commit_sha = ?`, commitSHA)
}

// ByCommitAndTree returns every row for exactly (commitSHA, treeHash),
// oldest first.
func (s *CiAttestationStore) ByCommitAndTree(ctx context.Context, commitSHA, treeHash string) ([]CiAttestationRow, error) {
	return s.query(ctx, `commit_sha = ? AND tree_hash = ?`, commitSHA, treeHash)
}

func (s *CiAttestationStore) query(ctx context.Context, where string, args ...any) ([]CiAttestationRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ciAttestationColumns+` FROM `+tableCiAttestation+
		` WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read ci_attestation")
	}
	defer func() { _ = rows.Close() }()
	var out []CiAttestationRow
	for rows.Next() {
		var r CiAttestationRow
		var accepted int64
		if err := rows.Scan(&r.ID, &r.NodeID, &r.RepoID, &r.CommitSHA, &r.TreeHash, &r.JobSpecHash, &r.ToolchainFP,
			&r.StartedAt, &r.EndedAt, &r.ExitStatus, &r.Signature, &r.CanonicalJSON, &r.PolicyVersion, &r.KeyID,
			&r.AttestorClass, &r.CheckpointID, &r.TargetSelection, &r.Nonce, &r.ExpiresAt, &accepted,
			&r.CreatedAt); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan ci_attestation")
		}
		r.AcceptedBeforeRevocation = accepted != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read ci_attestation")
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
