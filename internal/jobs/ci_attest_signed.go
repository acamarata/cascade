package jobs

// Purpose: the node-local tables behind ci.attest.sign (contract:node-attest-sign):
// ci_attest_run binds an execution id to the node's own ci_results run, the
// commit it ran and the checkout it ran in; ci_attest_signed records that a
// run has been signed, once.
//
// Inputs: the node attestor (internal/ci/attest_node.go).
// Outputs: CiAttestSignedStore.
// Constraints: both tables are keyed by the execution id; a second bind or a
// second sign of the same run is KindConflict and changes nothing.
//
// SPORT: internal.jobs.CiAttestSignedStore/ADDED (P1-CI-13).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	tableCiAttestRun    = "ci_attest_run"
	tableCiAttestSigned = "ci_attest_signed"
)

// NodeRun is one node-local CI run a ci.attest.sign request may name.
type NodeRun struct {
	ExecutionID string
	RunID       int64
	RepoID      int64
	CommitSHA   string
	Checkout    string
	RecordedAt  int64
}

// CiAttestSignedStore binds and marks node-local runs.
type CiAttestSignedStore struct{ db *sql.DB }

// NewCiAttestSignedStore returns a store over db, which must carry the jobs
// schema (ApplyJobsSchema).
func NewCiAttestSignedStore(db *sql.DB) (*CiAttestSignedStore, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: NewCiAttestSignedStore requires a non-nil db")
	}
	return &CiAttestSignedStore{db: db}, nil
}

func ciAttestRunTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_attest_run: the node's own CI runs, bound to the commit and checkout they ran",
		Table: &migrate.TableDef{
			Name: tableCiAttestRun,
			Columns: []migrate.ColumnDef{
				{Name: "execution_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "run_id", Type: migrate.TypeInteger, NotNull: true},
				{Name: "repo_id", Type: migrate.TypeInteger, NotNull: true},
				{Name: "commit_sha", Type: migrate.TypeText, NotNull: true},
				{Name: "checkout", Type: migrate.TypeText, NotNull: true},
				{Name: "recorded_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

func ciAttestSignedTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_attest_signed: one node signature per run",
		Table: &migrate.TableDef{
			Name: tableCiAttestSigned,
			Columns: []migrate.ColumnDef{
				{Name: "run_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "nonce", Type: migrate.TypeText, NotNull: true},
				{Name: "signed_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

// RecordRun binds run. An empty execution id, commit or checkout is
// KindInvalidInput; an execution id already bound is KindConflict.
func (s *CiAttestSignedStore) RecordRun(ctx context.Context, run NodeRun) error {
	if run.ExecutionID == "" || run.CommitSHA == "" || run.Checkout == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: a node run needs an execution id, a commit and a checkout")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO `+tableCiAttestRun+`
		(execution_id, run_id, repo_id, commit_sha, checkout, recorded_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		run.ExecutionID, run.RunID, run.RepoID, run.CommitSHA, run.Checkout, run.RecordedAt)
	return conflictUnlessInserted(res, err, "jobs: execution "+run.ExecutionID+" is already bound to a run")
}

// Run returns the run bound to executionID, or false when none is.
func (s *CiAttestSignedStore) Run(ctx context.Context, executionID string) (NodeRun, bool, error) {
	run := NodeRun{ExecutionID: executionID}
	err := s.db.QueryRowContext(ctx, `SELECT run_id, repo_id, commit_sha, checkout, recorded_at FROM `+tableCiAttestRun+`
		WHERE execution_id = ?`, executionID).Scan(&run.RunID, &run.RepoID, &run.CommitSHA, &run.Checkout, &run.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeRun{}, false, nil
	}
	if err != nil {
		return NodeRun{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read ci_attest_run")
	}
	return run, true, nil
}

// MarkSigned records that executionID was signed for nonce. A run already
// signed is KindConflict and changes nothing.
func (s *CiAttestSignedStore) MarkSigned(ctx context.Context, executionID, nonce string, signedAt int64) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO `+tableCiAttestSigned+` (run_id, nonce, signed_at) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, executionID, nonce, signedAt)
	return conflictUnlessInserted(res, err, "jobs: run "+executionID+" is already signed")
}

func conflictUnlessInserted(res sql.Result, err error, conflict string) error {
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: write node attestation state")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return cascade.New(cascade.KindConflict, conflict)
	}
	return nil
}
