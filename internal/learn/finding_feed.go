package learn

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EvidenceQuerier reads the latest structured evidence for a job and kind.
type EvidenceQuerier interface {
	Query(ctx context.Context, jobID string, kind jobs.EvidenceKind) (jobs.EvidenceRecord, error)
}

// FindingFeed converts failed tool checks into structured telemetry findings.
// The composition root must apply the learn-config finding-feed schema first.
type FindingFeed struct {
	db     *sql.DB
	ledger EvidenceQuerier
	writer FindingWriter
}

// NewFindingFeed constructs a feed over the outcomes database and evidence ledger.
// A SQLite writer must share db so findings and feed markers commit together.
func NewFindingFeed(db *sql.DB, ledger EvidenceQuerier, w FindingWriter) (*FindingFeed, error) {
	if db == nil || feedNil(ledger) || feedNil(w) {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: FindingFeed needs a db, ledger and writer")
	}
	if sw, ok := w.(*SQLiteFindingWriter); ok && sw.db != db {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: FindingFeed writer must share the outcomes db")
	}
	return &FindingFeed{db: db, ledger: ledger, writer: w}, nil
}

func feedNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() { //nolint:exhaustive // only the nilable kinds can hold a typed nil
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	default:
		return false
	}
}

type feedOutcome struct {
	id    int64
	jobID string
}

// Run writes findings once per outcome and returns the number committed.
// Only build, lint and test failures from controller-run evidence are tool
// failures. Review, approval, claims, pending and passing checks emit nothing.
// No artifact data, result hashes or other evidence strings become findings.
func (f *FindingFeed) Run(ctx context.Context) (int, error) {
	if f == nil || f.db == nil || feedNil(f.ledger) || feedNil(f.writer) {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: Run needs a constructed FindingFeed")
	}
	outcomes, err := f.unfed(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range outcomes {
		findings, err := f.findings(ctx, o.jobID)
		if err != nil {
			return n, err
		}
		written, err := f.commit(ctx, o, findings)
		if err != nil {
			return n, err
		}
		n += written
	}
	return n, nil
}

// unfed closes the cursor before consulting the ledger or starting writes;
// the ledger and writer may share a database with a single connection.
func (f *FindingFeed) unfed(ctx context.Context) ([]feedOutcome, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT o.id, o.job_id FROM jobs_telemetry_outcomes o
		WHERE NOT EXISTS (SELECT 1 FROM jobs_learn_finding_feed f WHERE f.outcome_id = o.id) ORDER BY o.id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read unfed outcomes")
	}
	defer func() { _ = rows.Close() }()
	var out []feedOutcome
	for rows.Next() {
		var o feedOutcome
		if err := rows.Scan(&o.id, &o.jobID); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan unfed outcome")
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: iterate unfed outcomes")
	}
	return out, nil
}

func (f *FindingFeed) findings(ctx context.Context, jobID string) ([]Finding, error) {
	var out []Finding
	for _, kind := range []jobs.EvidenceKind{jobs.EvidenceBuild, jobs.EvidenceLint, jobs.EvidenceTests} {
		rec, err := f.ledger.Query(ctx, jobID, kind)
		if errors.Is(err, jobs.ErrNoEvidence) {
			continue
		}
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: query finding evidence")
		}
		if rec.JobID != jobID || rec.Kind != kind || rec.Outcome != jobs.OutcomeFail ||
			rec.ProducerCapability != jobs.ProducerControllerRun {
			continue
		}
		// Evidence exposes no failure reason. Use the closed crash bucket
		// for a failed tool check; never parse output to infer a category.
		out = append(out, Finding{JobID: jobID, Family: FamilyToolFailure,
			Category: CategoryCrash, Severity: SeverityNormal, Count: 1})
	}
	return out, nil
}

func (f *FindingFeed) commit(ctx context.Context, o feedOutcome, findings []Finding) (int, error) {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: begin finding feed")
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO jobs_learn_finding_feed (outcome_id, fed_at)
		SELECT id, CAST(strftime('%s', 'now') AS INTEGER) FROM jobs_telemetry_outcomes WHERE id = ?
		ON CONFLICT(outcome_id) DO NOTHING`, o.id)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: claim finding feed outcome")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: count finding feed claim")
	}
	if n == 0 {
		return 0, nil
	}
	for _, finding := range findings {
		if err := f.write(ctx, tx, o.id, finding); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: commit finding feed")
	}
	return len(findings), nil
}

func (f *FindingFeed) write(ctx context.Context, tx *sql.Tx, outcomeID int64, finding Finding) error {
	if _, ok := f.writer.(*SQLiteFindingWriter); !ok {
		return f.writer.WriteFinding(ctx, finding)
	}
	// SQLiteFindingWriter has no transactional method. Its fixed structured
	// tuple is inserted on this transaction to make the marker atomic with
	// the finding, including on process failure and concurrent feed runs.
	if err := validateFindingInputs(finding); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs_telemetry_finding
		(outcome_id, family, category, severity, count) VALUES (?, ?, ?, ?, ?)`,
		outcomeID, finding.Family, finding.Category, finding.Severity, finding.Count)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: write feed finding")
	}
	return nil
}
