package jobs

// Purpose: typed CRUD for the job and task_dependency tables: PutJob
//
//	(insert or full replace, application-default 0 on
//	ConsecutiveFailedAttempts), GetJob, PutTransition (the only path that
//	MAY change Job.State, gated by TransitionAllowed), PutDependency, and
//	Dependencies.
//
// Inputs: an open *sql.DB already migrated via ApplyJobsSchema.
// Outputs: typed A-T7 errors for malformed, missing, illegal-transition,
//
//	or storage-failure paths; never a bare *sql.Rows leak.
//
// Constraints: PutTransition is the ONLY exported mutator of Job.State;
//
//	it refuses (typed KindConflict/KindPermissionDenied, per
//	TransitionAllowed) any transition not in the legal table, matching
//	this ticket's plan-named "unknown-transition" error path.
//
// SPORT: jobs/store/ADD (P1-E29-W6-S59-T1).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Store is the jobs domain's persisted CRUD surface, split across this
// file (job/task_dependency), store_exec.go (execution/execution_result/
// artifact) and store_lease.go (resource_lease/worktree) per the 300-line
// cap.
type Store struct {
	db *sql.DB

	// onTerminal, when non-nil, is invoked by PutTransition after a
	// transition lands a job in one of its four terminal states
	// (accepted|rejected|cancelled|failed) -- the DECIDED
	// release-on-terminal lease path (P1-E29-W6-S59-T2). Set via
	// SetTerminalHook by the composition root that also constructs this
	// Store's LeaseManager; nil (the default) means no lease release is
	// wired, which is correct for every caller that never acquires a
	// lease (store_job_test.go's plain transition tests, for one).
	onTerminal func(ctx context.Context, jobID string) error
}

// NewStore wraps db. db must already carry the MigrationSet tables
// (ApplyJobsSchema).
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SetTerminalHook installs fn as the release-on-terminal callback
// PutTransition invokes once a job reaches a terminal state. The
// composition root calls this with LeaseManager.releaseAllForJob after
// constructing both the Store and its LeaseManager -- there is no
// import-cycle-free way for Store itself to hold a *LeaseManager
// (LeaseManager already holds a *Store), so this seam is the injection
// point instead. Passing nil disables the hook (the zero-value
// behavior).
func (s *Store) SetTerminalHook(fn func(ctx context.Context, jobID string) error) {
	s.onTerminal = fn
}

// PutJob inserts or fully replaces one job row. A newly-created job
// (INSERT, not UPDATE) that leaves ConsecutiveFailedAttempts at its Go
// zero value persists 0 -- the application-layer default the DSL's lack
// of SQL DEFAULT requires (see migration.go's doc comment).
func (s *Store) PutJob(ctx context.Context, j Job) error {
	if j.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: job id is required")
	}
	if !j.State.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown job state %q", string(j.State))
	}
	if !j.ConsequenceClass.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown consequence_class %q", string(j.ConsequenceClass))
	}
	if !j.DataClass.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown data_class %q", string(j.DataClass))
	}
	caps, err := json.Marshal(j.Capabilities)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "jobs: encode capabilities")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableJob+` (id, state, created_at, updated_at, capabilities,
			mutable_scope, risk_class, min_task_class, node_requirements,
			timeout_seconds, cost_ceiling, priority, consecutive_failed_attempts,
			consequence_class, data_class)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET state=excluded.state, updated_at=excluded.updated_at,
			capabilities=excluded.capabilities, mutable_scope=excluded.mutable_scope,
			risk_class=excluded.risk_class, min_task_class=excluded.min_task_class,
			node_requirements=excluded.node_requirements, timeout_seconds=excluded.timeout_seconds,
			cost_ceiling=excluded.cost_ceiling, priority=excluded.priority,
			consecutive_failed_attempts=excluded.consecutive_failed_attempts,
			consequence_class=excluded.consequence_class, data_class=excluded.data_class`,
		j.ID, string(j.State), j.CreatedAt, j.UpdatedAt, string(caps),
		j.MutableScope, j.RiskClass, j.MinTaskClass, j.NodeRequirements,
		j.TimeoutSeconds, j.CostCeiling, j.Priority, j.ConsecutiveFailedAttempts,
		string(j.ConsequenceClass), string(j.DataClass))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put job")
	}
	return nil
}

// GetJob reads one job row by id. ok=false with a nil error means "no
// such job" -- the normal, expected case, never itself an error.
func (s *Store) GetJob(ctx context.Context, id string) (Job, bool, error) {
	var j Job
	var state, capsRaw, consequence, data string
	row := s.db.QueryRowContext(ctx,
		`SELECT id, state, created_at, updated_at, capabilities, mutable_scope, risk_class,
			min_task_class, node_requirements, timeout_seconds, cost_ceiling, priority,
			consecutive_failed_attempts, consequence_class, data_class
		 FROM `+tableJob+` WHERE id = ?`, id)
	err := row.Scan(&j.ID, &state, &j.CreatedAt, &j.UpdatedAt, &capsRaw, &j.MutableScope,
		&j.RiskClass, &j.MinTaskClass, &j.NodeRequirements, &j.TimeoutSeconds, &j.CostCeiling,
		&j.Priority, &j.ConsecutiveFailedAttempts, &consequence, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: get job")
	}
	j.State = DecodeJobState(state)
	if j.ConsequenceClass, err = DecodeConsequenceClass(consequence); err != nil {
		return Job{}, false, err
	}
	if j.DataClass, err = DecodeDataClass(data); err != nil {
		return Job{}, false, err
	}
	if err := json.Unmarshal([]byte(capsRaw), &j.Capabilities); err != nil {
		return Job{}, false, cascade.Wrap(cascade.KindIntegrity, err, "jobs: decode capabilities")
	}
	return j, true, nil
}

// PutTransition is the ONLY exported path that may change a job's
// state. It refuses (typed error, per TransitionAllowed) any transition
// not in the public legal table -- including every policy-reserved edge
// -- and refuses when the job does not exist.
func (s *Store) PutTransition(ctx context.Context, id string, to JobState, updatedAt int64) error {
	j, ok, err := s.GetJob(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "jobs: job %q not found", id)
	}
	if err := TransitionAllowed(j.State, to); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE `+tableJob+` SET state = ?, updated_at = ? WHERE id = ?`,
		string(to), updatedAt, id)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put transition")
	}
	if to.Terminal() && s.onTerminal != nil {
		if err := s.onTerminal(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// PutDependency records one deps[] edge: job depends on dependsOnJobID.
// Both ids must reference existing job rows (store-enforced via the
// jobs_task_dependency table's FOREIGN KEY constraints).
func (s *Store) PutDependency(ctx context.Context, d TaskDependency) error {
	if d.JobID == "" || d.DependsOnJobID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: task_dependency job_id and depends_on_job_id are required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableTaskDependency+` (job_id, depends_on_job_id) VALUES (?, ?)
		 ON CONFLICT(job_id, depends_on_job_id) DO NOTHING`,
		d.JobID, d.DependsOnJobID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put dependency")
	}
	return nil
}

// Dependencies lists every job jobID depends on.
func (s *Store) Dependencies(ctx context.Context, jobID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT depends_on_job_id FROM `+tableTaskDependency+` WHERE job_id = ? ORDER BY depends_on_job_id`, jobID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list dependencies")
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan dependency")
		}
		out = append(out, dep)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate dependencies")
	}
	return out, nil
}
