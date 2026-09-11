package jobs

// Purpose: typed CRUD for execution, execution_result and artifact.
//
//	PutExecution store-enforces referential integrity against jobs_job
//	(the plan-named orphan-execution error path); IntegrityCheck reports
//	any orphaned execution rows a caller wants to audit directly.
//	PutArtifact store-enforces DataClass immutability (R-21.94): an
//	UPDATE that would change an existing artifact's data_class is
//	refused with a typed error.
//
// Inputs: an open *sql.DB already migrated via ApplyJobsSchema.
// Outputs: typed A-T7 errors; KindIntegrity for the orphan-execution and
//
//	immutability paths, KindNotFound/KindInvalidInput/KindUnavailable
//	for the rest.
//
// Constraints: PutExecution checks job existence itself rather than
//
//	relying solely on SQLite's FOREIGN KEY enforcement, so the same
//	typed error is returned regardless of whether the caller's *sql.DB
//	was opened with foreign_keys pragma support (providers/sqlite.Open
//	sets it; a bare database/sql.Open("sqlite", ...) in a test may not).
//
// SPORT: jobs/store/ADD (P1-E29-W6-S59-T1).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PutExecution inserts or replaces one execution row. Returns a typed
// KindIntegrity "orphan execution" error when JobID does not reference
// an existing jobs_job row -- this is the plan-named error path,
// checked explicitly rather than left to SQLite's own FK enforcement.
func (s *Store) PutExecution(ctx context.Context, e Execution) error {
	if e.ID == "" || e.JobID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: execution id and job_id are required")
	}
	if !e.State.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown execution state %q", string(e.State))
	}
	_, ok, err := s.GetJob(ctx, e.JobID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindIntegrity, "jobs: orphan execution %q: job %q does not exist", e.ID, e.JobID)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableExecution+` (id, job_id, attempt, state, started_at, ended_at, pgid, heartbeat_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET state=excluded.state, started_at=excluded.started_at,
			ended_at=excluded.ended_at, pgid=excluded.pgid, heartbeat_at=excluded.heartbeat_at`,
		e.ID, e.JobID, e.Attempt, string(e.State), e.StartedAt, e.EndedAt, e.PGID, e.HeartbeatAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put execution")
	}
	return nil
}

// GetExecution reads one execution row by id.
func (s *Store) GetExecution(ctx context.Context, id string) (Execution, bool, error) {
	var e Execution
	var state string
	row := s.db.QueryRowContext(ctx,
		`SELECT id, job_id, attempt, state, started_at, ended_at, pgid, heartbeat_at
		 FROM `+tableExecution+` WHERE id = ?`, id)
	err := row.Scan(&e.ID, &e.JobID, &e.Attempt, &state, &e.StartedAt, &e.EndedAt, &e.PGID, &e.HeartbeatAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Execution{}, false, nil
	}
	if err != nil {
		return Execution{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: get execution")
	}
	e.State = DecodeExecutionState(state)
	return e, true, nil
}

// IntegrityCheck reports the ids of every jobs_execution row whose
// job_id does not reference an existing jobs_job row -- the plan-named
// audit surface, usable even against a database populated by a path
// other than PutExecution (e.g. a restored backup).
func (s *Store) IntegrityCheck(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id FROM `+tableExecution+` e
		 LEFT JOIN `+tableJob+` j ON e.job_id = j.id
		 WHERE j.id IS NULL ORDER BY e.id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: integrity check")
	}
	defer func() { _ = rows.Close() }()
	var orphans []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan orphan execution")
		}
		orphans = append(orphans, id)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate orphan executions")
	}
	return orphans, nil
}

// PutExecutionResult inserts or replaces one execution_result row.
func (s *Store) PutExecutionResult(ctx context.Context, r ExecutionResult) error {
	if r.ExecutionID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: execution_result execution_id is required")
	}
	refs, err := json.Marshal(r.ArtifactRefs)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode artifact_refs")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableExecutionResult+` (execution_id, output_summary, error_kind, error_message, artifact_refs)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(execution_id) DO UPDATE SET output_summary=excluded.output_summary,
			error_kind=excluded.error_kind, error_message=excluded.error_message,
			artifact_refs=excluded.artifact_refs`,
		r.ExecutionID, r.OutputSummary, r.ErrorKind, r.ErrorMessage, string(refs))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put execution result")
	}
	return nil
}

// PutArtifact inserts a new artifact row, or updates an existing one.
// DataClass is IMMUTABLE (R-21.94): an update whose DataClass differs
// from the stored row's is refused with a typed KindIntegrity error and
// no columns are written.
func (s *Store) PutArtifact(ctx context.Context, a Artifact) error {
	if a.ID == "" || a.JobID == "" || a.ExecutionID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: artifact id, job_id and execution_id are required")
	}
	if !a.ConsequenceClass.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown consequence_class %q", string(a.ConsequenceClass))
	}
	if !a.DataClass.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown data_class %q", string(a.DataClass))
	}
	existing, ok, err := s.GetArtifact(ctx, a.ID)
	if err != nil {
		return err
	}
	if ok && existing.DataClass != a.DataClass {
		return cascade.Newf(cascade.KindIntegrity,
			"jobs: artifact %q data_class is immutable: stored %q, refusing update to %q",
			a.ID, existing.DataClass, a.DataClass)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableArtifact+` (id, job_id, execution_id, kind, blob_key, path, consequence_class, data_class)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, blob_key=excluded.blob_key,
			path=excluded.path, consequence_class=excluded.consequence_class`,
		a.ID, a.JobID, a.ExecutionID, a.Kind, a.BlobKey, a.Path, string(a.ConsequenceClass), string(a.DataClass))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put artifact")
	}
	return nil
}

// GetArtifact reads one artifact row by id.
func (s *Store) GetArtifact(ctx context.Context, id string) (Artifact, bool, error) {
	var a Artifact
	var consequence, data string
	row := s.db.QueryRowContext(ctx,
		`SELECT id, job_id, execution_id, kind, blob_key, path, consequence_class, data_class
		 FROM `+tableArtifact+` WHERE id = ?`, id)
	err := row.Scan(&a.ID, &a.JobID, &a.ExecutionID, &a.Kind, &a.BlobKey, &a.Path, &consequence, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: get artifact")
	}
	if a.ConsequenceClass, err = DecodeConsequenceClass(consequence); err != nil {
		return Artifact{}, false, err
	}
	if a.DataClass, err = DecodeDataClass(data); err != nil {
		return Artifact{}, false, err
	}
	return a, true, nil
}
