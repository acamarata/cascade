package jobs

// Purpose: the jobs_evidence_class side table's schema -- stores each
//
//	evidence row's effective data class outside the hash chain so the
//	class can be raised without breaking Verify. The class is the lattice
//	max of the record's class and the attested job's DataClass at append
//	time; an empty record class takes the job's class; an unreadable job
//	or unknown value stores SensitivityLocalOnly (fail closed).
//
// SPORT: jobs/evidence-ledger-read/ADD (P1-PBD-06).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const tableEvidenceClass = "jobs_evidence_class"

// evidenceClassSchemaVersion is the base evidence schema version + 1.
const evidenceClassSchemaVersion = evidenceSchemaVersion

func evidenceClassStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_evidence_class: per-row effective data class (P1-PBD-06)",
		Table: &migrate.TableDef{Name: tableEvidenceClass, Columns: []migrate.ColumnDef{
			{Name: "job_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "seq", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
			{Name: "data_class", Type: migrate.TypeText, NotNull: true},
		}},
	}
}

func evidenceClassWord(rec EvidenceRecord) string {
	word, _ := provider.DataClassName(provider.TierOrLocalOnly(rec.DataClass))
	return word
}

func (l *EvidenceLedger) readEvidenceClass(ctx context.Context, rec *EvidenceRecord) error {
	var word string
	err := l.store.db.QueryRowContext(ctx, `SELECT data_class FROM jobs_evidence_class WHERE job_id=? AND seq=?`, rec.JobID, rec.Seq).Scan(&word)
	if err == sql.ErrNoRows {
		class := l.jobEvidenceClass(ctx, rec.JobID)
		rec.DataClass = &class
		return nil
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: read evidence class")
	}
	class := tierOrLocalOnly(word)
	rec.DataClass = &class
	return nil
}

// insertEvidenceClassRowTx writes one class row inside the caller's transaction.
func insertEvidenceClassRowTx(ctx context.Context, tx *sql.Tx, jobID string, seq int64, class string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO `+tableEvidenceClass+` (job_id, seq, data_class) VALUES (?,?,?)`,
		jobID, seq, class)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: insert evidence class row")
	}
	return nil
}

// queryEvidenceClassTx reads the stored class word for one row.
func queryEvidenceClassTx(ctx context.Context, tx *sql.Tx, jobID string, seq int64) (string, bool, error) {
	var class string
	err := tx.QueryRowContext(ctx, `SELECT data_class FROM `+tableEvidenceClass+` WHERE job_id = ? AND seq = ?`,
		jobID, seq).Scan(&class)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: query evidence class")
	}
	return class, true, nil
}
