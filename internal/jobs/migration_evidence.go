package jobs

// Purpose: the jobs_evidence table's schema (via the B/S-02.T3 portable
//
//	migration builder, following migration.go's own precedent) plus the
//	row-level SQL this ticket's evidence.go needs: idempotency-key
//	lookup, prev_hash read, seq assignment, insert, scan and the R-21.183
//	chain-verify walk.
//
// SetID (R-16.77): "jobs" is already claimed by migration.go's
// MigrationSet(); this file claims the DISTINCT "jobs-evidence" SetID at
// schema_version 1, its own independent sequence under the new
// (SetID, schema_version) ledger key.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableEvidence = "jobs_evidence"

const evidenceSchemaVersion = 1

// EvidenceMigrationSet is the jobs_evidence table's own MigrationSet,
// distinct from MigrationSet() (migration.go) per R-16.77.
func EvidenceMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "jobs-evidence",
		SchemaVersion: evidenceSchemaVersion,
		ReaderCeiling: evidenceSchemaVersion,
		Steps: []migrate.MigrationStep{
			{
				Kind:        migrate.StepCreateTable,
				Description: "jobs_evidence: per-job append-only evidence ledger (R-21.145/R-21.183)",
				Table: &migrate.TableDef{
					Name: tableEvidence,
					Columns: []migrate.ColumnDef{
						{Name: "job_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
						{Name: "seq", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
						{Name: "kind", Type: migrate.TypeText, NotNull: true},
						{Name: "producer_capability", Type: migrate.TypeText, NotNull: true},
						{Name: "attempt_id", Type: migrate.TypeText, NotNull: true},
						{Name: "checkpoint_id", Type: migrate.TypeText, NotNull: true},
						{Name: "tree_hash", Type: migrate.TypeText, NotNull: true},
						{Name: "attestor_identity", Type: migrate.TypeText, NotNull: true},
						{Name: "prev_hash", Type: migrate.TypeBlob, NotNull: true},
						{Name: "started_at", Type: migrate.TypeInteger, NotNull: true},
						{Name: "ended_at", Type: migrate.TypeInteger, NotNull: true},
						{Name: "outcome", Type: migrate.TypeText, NotNull: true},
						{Name: "result_hash", Type: migrate.TypeText, NotNull: true},
						{Name: "idempotency_key", Type: migrate.TypeText, NotNull: true},
						{Name: "artifact_ref", Type: migrate.TypeText, NotNull: true},
						{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
					},
					ForeignKeys: []migrate.ForeignKeyDef{
						{Column: "job_id", RefTable: tableJob, RefColumn: "id"},
					},
				},
			},
		},
	}
}

// ApplyEvidenceSchema applies EvidenceMigrationSet against db.
func ApplyEvidenceSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyEvidenceSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyEvidenceSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, EvidenceMigrationSet())
}

// existingByIdempotencyKeyTx returns the row already carrying key for
// jobID, if any -- the append-only no-op path.
func existingByIdempotencyKeyTx(ctx context.Context, tx *sql.Tx, jobID, key string) (EvidenceRecord, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT seq, kind, producer_capability, attempt_id, checkpoint_id, tree_hash,
		attestor_identity, prev_hash, started_at, ended_at, outcome, result_hash, idempotency_key, artifact_ref, created_at
		FROM `+tableEvidence+` WHERE job_id = ? AND idempotency_key = ?`, jobID, key)
	var rec EvidenceRecord
	var kind string
	var prev []byte
	var started, ended, created int64
	err := row.Scan(&rec.Seq, &kind, &rec.ProducerCapability, &rec.AttemptID, &rec.CheckpointID, &rec.TreeHash,
		&rec.AttestorIdentity, &prev, &started, &ended, &rec.Outcome, &rec.ResultHash, &rec.IdempotencyKey, &rec.ArtifactRef, &created)
	if err == sql.ErrNoRows {
		return EvidenceRecord{}, false, nil
	}
	if err != nil {
		return EvidenceRecord{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: idempotency lookup")
	}
	rec.JobID = jobID
	rec.Kind = EvidenceKind(kind)
	rec.PrevHash = prev
	rec.StartedAt = unixToTime(started)
	rec.EndedAt = unixToTime(ended)
	rec.CreatedAt = unixToTime(created)
	return rec, true, nil
}

// prevHashTx returns the chain-hash of jobID's latest row, or the R-21.183
// zero-byte genesis when jobID has none yet.
func prevHashTx(ctx context.Context, tx *sql.Tx, jobID string) ([]byte, error) {
	last, ok, err := lastRowTx(ctx, tx, jobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return append([]byte{}, genesisHash...), nil
	}
	return chainHash(last)
}

func lastRowTx(ctx context.Context, tx *sql.Tx, jobID string) (EvidenceRecord, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT seq, kind, producer_capability, attempt_id, checkpoint_id, tree_hash,
		attestor_identity, prev_hash, started_at, ended_at, outcome, result_hash, idempotency_key, artifact_ref, created_at
		FROM `+tableEvidence+` WHERE job_id = ? ORDER BY seq DESC LIMIT 1`, jobID)
	return scanEvidenceRowTx(row, jobID)
}

func scanEvidenceRowTx(row *sql.Row, jobID string) (EvidenceRecord, bool, error) {
	var rec EvidenceRecord
	var kind string
	var prev []byte
	var started, ended, created int64
	err := row.Scan(&rec.Seq, &kind, &rec.ProducerCapability, &rec.AttemptID, &rec.CheckpointID, &rec.TreeHash,
		&rec.AttestorIdentity, &prev, &started, &ended, &rec.Outcome, &rec.ResultHash, &rec.IdempotencyKey, &rec.ArtifactRef, &created)
	if err == sql.ErrNoRows {
		return EvidenceRecord{}, false, nil
	}
	if err != nil {
		return EvidenceRecord{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan evidence row")
	}
	rec.JobID = jobID
	rec.Kind = EvidenceKind(kind)
	rec.PrevHash = prev
	rec.StartedAt = unixToTime(started)
	rec.EndedAt = unixToTime(ended)
	rec.CreatedAt = unixToTime(created)
	return rec, true, nil
}

func scanEvidenceRow(row *sql.Row, jobID string, kind EvidenceKind) (EvidenceRecord, error) {
	var rec EvidenceRecord
	var prev []byte
	var started, ended, created int64
	err := row.Scan(&rec.Seq, &rec.ProducerCapability, &rec.AttemptID, &rec.CheckpointID, &rec.TreeHash,
		&rec.AttestorIdentity, &prev, &started, &ended, &rec.Outcome, &rec.ResultHash, &rec.IdempotencyKey, &rec.ArtifactRef, &created)
	if err != nil {
		return EvidenceRecord{}, err
	}
	rec.JobID = jobID
	rec.Kind = kind
	rec.PrevHash = prev
	rec.StartedAt = unixToTime(started)
	rec.EndedAt = unixToTime(ended)
	rec.CreatedAt = unixToTime(created)
	return rec, nil
}

func nextSeqTx(ctx context.Context, tx *sql.Tx, jobID string) (int64, error) {
	var seq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM `+tableEvidence+` WHERE job_id = ?`, jobID).Scan(&seq); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read next seq")
	}
	return seq.Int64 + 1, nil
}

func insertEvidenceRowTx(ctx context.Context, tx *sql.Tx, rec EvidenceRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO `+tableEvidence+` (job_id, seq, kind, producer_capability,
		attempt_id, checkpoint_id, tree_hash, attestor_identity, prev_hash, started_at, ended_at, outcome,
		result_hash, idempotency_key, artifact_ref, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.JobID, rec.Seq, string(rec.Kind), string(rec.ProducerCapability), rec.AttemptID, rec.CheckpointID,
		rec.TreeHash, rec.AttestorIdentity, rec.PrevHash, rec.StartedAt.Unix(), rec.EndedAt.Unix(), string(rec.Outcome),
		rec.ResultHash, rec.IdempotencyKey, rec.ArtifactRef, rec.CreatedAt.Unix())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: insert evidence row")
	}
	return nil
}

func unixToTime(u int64) time.Time {
	return time.Unix(u, 0).UTC()
}

// Verify walks jobID's chain from genesis to Cursor(), recomputing every
// link. A link mismatch, a seq gap, or a wrong genesis value is a typed
// ErrEvidenceChainBroken naming the FIRST bad seq -- never a silent
// repair, never a warning.
func (l *EvidenceLedger) Verify(ctx context.Context, jobID string) error {
	rows, err := l.store.db.QueryContext(ctx, `SELECT seq, kind, producer_capability, attempt_id, checkpoint_id,
		tree_hash, attestor_identity, prev_hash, started_at, ended_at, outcome, result_hash, idempotency_key,
		artifact_ref, created_at FROM `+tableEvidence+` WHERE job_id = ? ORDER BY seq ASC`, jobID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: verify chain query")
	}
	defer func() { _ = rows.Close() }()

	wantPrev := append([]byte{}, genesisHash...)
	wantSeq := int64(1)
	for rows.Next() {
		var rec EvidenceRecord
		var kind string
		var prev []byte
		var started, ended, created int64
		if err := rows.Scan(&rec.Seq, &kind, &rec.ProducerCapability, &rec.AttemptID, &rec.CheckpointID,
			&rec.TreeHash, &rec.AttestorIdentity, &prev, &started, &ended, &rec.Outcome, &rec.ResultHash,
			&rec.IdempotencyKey, &rec.ArtifactRef, &created); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: verify chain scan")
		}
		rec.JobID = jobID
		rec.Kind = EvidenceKind(kind)
		rec.StartedAt = unixToTime(started)
		rec.EndedAt = unixToTime(ended)
		rec.CreatedAt = unixToTime(created)

		if rec.Seq != wantSeq {
			return cascade.Wrapf(cascade.KindIntegrity, ErrEvidenceChainBroken, "jobs: evidence chain for %s: seq gap at %d", jobID, wantSeq)
		}
		if !bytesEqual(prev, wantPrev) {
			return cascade.Wrapf(cascade.KindIntegrity, ErrEvidenceChainBroken, "jobs: evidence chain for %s: broken link at seq %d", jobID, rec.Seq)
		}
		rec.PrevHash = prev
		nextPrev, err := chainHash(rec)
		if err != nil {
			return err
		}
		wantPrev = nextPrev
		wantSeq++
	}
	if err := rows.Err(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: verify chain iterate")
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Query returns jobID's latest record of kind, or ErrNoEvidence.
func (l *EvidenceLedger) Query(ctx context.Context, jobID string, kind EvidenceKind) (EvidenceRecord, error) {
	row := l.store.db.QueryRowContext(ctx, `SELECT seq, producer_capability, attempt_id, checkpoint_id, tree_hash,
		attestor_identity, prev_hash, started_at, ended_at, outcome, result_hash, idempotency_key, artifact_ref, created_at
		FROM `+tableEvidence+` WHERE job_id = ? AND kind = ? ORDER BY seq DESC LIMIT 1`, jobID, string(kind))
	rec, err := scanEvidenceRow(row, jobID, kind)
	if err == sql.ErrNoRows {
		return EvidenceRecord{}, ErrNoEvidence
	}
	if err != nil {
		return EvidenceRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "jobs: query evidence")
	}
	return rec, nil
}

// Cursor returns jobID's current max seq (0 for an empty ledger).
func (l *EvidenceLedger) Cursor(ctx context.Context, jobID string) (int64, error) {
	var seq sql.NullInt64
	err := l.store.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM `+tableEvidence+` WHERE job_id = ?`, jobID).Scan(&seq)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read evidence cursor")
	}
	return seq.Int64, nil
}
