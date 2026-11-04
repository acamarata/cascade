package learn

// Purpose: read structured terminal outcomes and their findings without writes.
// Inputs: an injected database and inclusive UTC window endpoints.
// Outputs: one OutcomeRow per outcome, with validated finding enums.
// Constraints: no prompt text; SQL failures never become empty evidence.
// SPORT: learn/outcome-reader/ADD.

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// OutcomeReader reads terminal outcomes within an inclusive window.
type OutcomeReader interface {
	ReadWindow(ctx context.Context, from, to time.Time) ([]OutcomeRow, error)
}

// SQLiteOutcomeReader reads the existing telemetry schema, using created_at
// as the terminal outcome timestamp supplied by the outcome writer.
type SQLiteOutcomeReader struct{ db *sql.DB }

// NewSQLiteOutcomeReader constructs a reader over db.
func NewSQLiteOutcomeReader(db *sql.DB) *SQLiteOutcomeReader {
	return &SQLiteOutcomeReader{db: db}
}

// ReadWindow joins findings without multiplying outcome occurrences.
// Malformed rows and unavailable storage refuse the entire read.
func (r *SQLiteOutcomeReader) ReadWindow(ctx context.Context, from, to time.Time) ([]OutcomeRow, error) {
	if r == nil || r.db == nil || from.IsZero() || to.Before(from) {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: invalid outcome window or reader")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT o.id, o.job_id, o.repo_id,
		o.component, o.task_class, o.risk_class, o.lane_tier, o.node_id,
		o.created_at, f.family, f.category, f.severity, f.count
		FROM jobs_telemetry_outcomes o LEFT JOIN jobs_telemetry_finding f ON f.outcome_id = o.id
		WHERE o.created_at >= ? AND o.created_at <= ? ORDER BY o.created_at, o.id, f.id`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read outcome window")
	}
	defer func() { _ = rows.Close() }()
	var out []OutcomeRow
	for rows.Next() {
		o, err := scanOutcomeFinding(rows)
		if err != nil {
			return nil, err
		}
		if o.CompletedAt.Before(from) || o.CompletedAt.After(to) {
			continue
		}
		if len(out) > 0 && out[len(out)-1].OutcomeID == o.OutcomeID {
			out[len(out)-1].Findings = append(out[len(out)-1].Findings, o.Findings...)
		} else {
			out = append(out, o)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: iterate outcome window")
	}
	return out, nil
}

func scanOutcomeFinding(rows *sql.Rows) (OutcomeRow, error) {
	var o OutcomeRow
	var at int64
	var family, category, severity sql.NullString
	var count sql.NullInt64
	err := rows.Scan(&o.OutcomeID, &o.JobID, &o.RepoID, &o.Component, &o.TaskClass,
		&o.RiskClass, &o.Lane.LaneTier, &o.Lane.NodeID, &at, &family, &category, &severity, &count)
	if err != nil {
		return o, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan outcome window")
	}
	o.CompletedAt = time.Unix(at, 0).UTC()
	if err := validatePatternRow(o); err != nil {
		return o, err
	}
	if !family.Valid && !category.Valid && !severity.Valid && !count.Valid {
		return o, nil
	}
	if !family.Valid || !category.Valid || !severity.Valid || !count.Valid || count.Int64 < 0 {
		return o, cascade.New(cascade.KindInvalidInput, "learn: malformed stored finding")
	}
	f, c, err := DecodeFamilyCategory(family.String, category.String)
	if err != nil {
		return o, err
	}
	s, err := DecodeSeverity(severity.String)
	if err != nil {
		return o, err
	}
	o.Findings = []Finding{{JobID: o.JobID, Family: f, Category: c, Severity: s, Count: int(count.Int64)}}
	return o, nil
}

func validatePatternRow(o OutcomeRow) error {
	if o.OutcomeID <= 0 || o.JobID == "" || !jobIDPattern.MatchString(o.JobID) {
		return cascade.New(cascade.KindInvalidInput, "learn: invalid outcome evidence identity")
	}
	t := TelemetryOutcome{JobID: o.JobID, RepoID: o.RepoID, Component: o.Component,
		TaskClass: o.TaskClass, RiskClass: o.RiskClass, LaneTier: o.Lane.LaneTier, NodeID: o.Lane.NodeID}
	if err := validateNoCredentialShapes(t); err != nil {
		return err
	}
	return validateIdentifyingColumns(t)
}
