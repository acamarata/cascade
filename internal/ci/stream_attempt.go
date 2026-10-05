// Purpose (this file): the ci_stream_attempt store -- one row per
// checkpoint attempt, with the one-live-attempt-per-lease rule, the
// tombstone and terminal transitions, and the append-only list of dispatch
// requests Resume replays after a crash.
//
// Inputs: an open *sql.DB migrated through MigrationSet.
// Outputs: attemptRow values and typed errors (ErrLateResult for an
// attempt that is not live).
// Constraints: at most one non-tombstoned row per lease_id. The rule is enforced
// inside one transaction (the migrate DSL has no partial unique index):
// opening an attempt tombstones every other live row of its lease first.
// A tombstoned or terminal attempt never becomes live again. The
// `dispatches` column carries the JSON dispatch requests (ref, snapshot,
// plan, kinds) so a restarted process can re-dispatch an interrupted
// sub-job; it is an addition to the contract's column list, disclosed in
// docs/ci/streaming.md. No transaction here ever calls back into the
// database handle (the stores run over one connection).
// SPORT: internal.ci.ci_stream_attempt/ADDED (P1-CI-01).

package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableStreamAttempt = "ci_stream_attempt"

// The closed attempt state vocabulary.
const (
	attemptLive       = "live"
	attemptTombstoned = "tombstoned"
	attemptTerminal   = "terminal"
)

// dispatchEntry is one recorded Dispatch request: everything needed to
// rebuild its SubJobs. Kinds is the explicit kind list (never re-derived
// from Plan.Requirement, whose zero value means "all").
type dispatchEntry struct {
	Ref        JobRef            `json:"ref"`
	Snapshot   CandidateSnapshot `json:"snapshot"`
	Plan       CIRequirementPlan `json:"plan"`
	Acceptance bool              `json:"acceptance"`
	Kinds      []RequirementKind `json:"kinds"`
	Risk       string            `json:"risk"`
}

// attemptRow is one ci_stream_attempt row with its decoded dispatches.
type attemptRow struct {
	AttemptID, JobID, LeaseID, CheckpointID, TreeHash, State string
	Entries                                                  []dispatchEntry
	CreatedAt, UpdatedAt                                     int64
}

func attemptTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_stream_attempt: one row per streaming checkpoint attempt; at most one live row per lease",
		Table: &migrate.TableDef{
			Name: tableStreamAttempt,
			Columns: []migrate.ColumnDef{
				{Name: "attempt_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "job_id", Type: migrate.TypeText, NotNull: true},
				{Name: "lease_id", Type: migrate.TypeText, NotNull: true},
				{Name: "checkpoint_id", Type: migrate.TypeText, NotNull: true},
				{Name: "tree_hash", Type: migrate.TypeText, NotNull: true},
				{Name: "state", Type: migrate.TypeText, NotNull: true},
				{Name: "dispatches", Type: migrate.TypeText, NotNull: true},
				{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "updated_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

// leaseKey is the ci_stream_attempt.lease_id of a lease.
func leaseKey(l jobs.ResourceLease) string { return l.RepoID + "\x1f" + l.ScopeGlob }

const attemptColumns = `attempt_id, job_id, lease_id, checkpoint_id, tree_hash, state, dispatches, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanAttempt(sc rowScanner) (attemptRow, error) {
	var r attemptRow
	var raw string
	if err := sc.Scan(&r.AttemptID, &r.JobID, &r.LeaseID, &r.CheckpointID, &r.TreeHash, &r.State, &raw, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return attemptRow{}, err
	}
	if err := json.Unmarshal([]byte(raw), &r.Entries); err != nil {
		return attemptRow{}, cascade.Wrapf(cascade.KindIntegrity, err, "ci: stream attempt %s: unreadable dispatches", r.AttemptID)
	}
	return r, nil
}

// queryAttempts runs a SELECT over ci_stream_attempt and decodes every row.
func queryAttempts(ctx context.Context, db *sql.DB, where string, args ...any) ([]attemptRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+attemptColumns+` FROM `+tableStreamAttempt+` `+where, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: query attempts")
	}
	defer func() { _ = rows.Close() }()
	var out []attemptRow
	for rows.Next() {
		r, err := scanAttempt(rows)
		if err != nil {
			return nil, asKind(err, cascade.KindUnavailable, "ci: stream: scan attempt")
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: iterate attempts")
	}
	return out, nil
}

// asKind wraps err under kind unless it already is a taxonomy error.
func asKind(err error, kind cascade.Kind, msg string) error {
	if err == nil {
		return nil
	}
	var ce *cascade.Error
	if errors.As(err, &ce) {
		return err
	}
	return cascade.Wrap(kind, err, msg)
}

// getAttempt returns the attempt with id, or ok=false when none exists.
func (d *Dispatcher) getAttempt(ctx context.Context, id string) (attemptRow, bool, error) {
	rows, err := queryAttempts(ctx, d.deps.CIDB, `WHERE attempt_id = ?`, id)
	if err != nil || len(rows) == 0 {
		return attemptRow{}, false, err
	}
	return rows[0], true, nil
}

// currentForLease returns the lease's one current (not tombstoned)
// attempt, if any.
func (d *Dispatcher) currentForLease(ctx context.Context, lease string) (attemptRow, bool, error) {
	rows, err := queryAttempts(ctx, d.deps.CIDB, `WHERE lease_id = ? AND state != ? ORDER BY created_at DESC LIMIT 1`, lease, attemptTombstoned)
	if err != nil || len(rows) == 0 {
		return attemptRow{}, false, err
	}
	return rows[0], true, nil
}

// tombstoneOthers tombstones the lease's current (live or terminal)
// attempts whose checkpoint is not keep: a new checkpoint supersedes them.
func (d *Dispatcher) tombstoneOthers(ctx context.Context, lease, keep string) error {
	_, err := d.deps.CIDB.ExecContext(ctx,
		`UPDATE `+tableStreamAttempt+` SET state = ?, updated_at = ? WHERE lease_id = ? AND state != ? AND checkpoint_id != ?`,
		attemptTombstoned, d.nowMillis(), lease, attemptTombstoned, keep)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: tombstone attempts")
	}
	return nil
}

// tombstone retires one attempt (a fenced lease epoch): its rows are
// dropped by Resume and a late result against it is refused.
func (d *Dispatcher) tombstone(ctx context.Context, attemptID string) error {
	_, err := d.deps.CIDB.ExecContext(ctx,
		`UPDATE `+tableStreamAttempt+` SET state = ?, updated_at = ? WHERE attempt_id = ? AND state != ?`,
		attemptTombstoned, d.nowMillis(), attemptID, attemptTombstoned)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: tombstone fenced attempt")
	}
	return nil
}

// refuseSuperseded refuses a checkpoint that was opened before and whose
// every attempt has since been tombstoned: a newer checkpoint superseded it
// and it must not tombstone that newer one in turn.
func (d *Dispatcher) refuseSuperseded(ctx context.Context, checkpointID string) error {
	rows, err := queryAttempts(ctx, d.deps.CIDB, `WHERE checkpoint_id = ?`, checkpointID)
	if err != nil || len(rows) == 0 {
		return err
	}
	for _, r := range rows {
		if r.State != attemptTombstoned {
			return nil
		}
	}
	return cascade.Wrapf(cascade.KindConflict, ErrCheckpointStale, "ci: stream: checkpoint %s was superseded", checkpointID)
}

// openAttempt makes row the lease's one live attempt: in one transaction
// every other live row of the lease is tombstoned, then row is inserted.
func (d *Dispatcher) openAttempt(ctx context.Context, row attemptRow) error {
	tx, err := d.deps.CIDB.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: begin attempt")
	}
	defer func() { _ = tx.Rollback() }()
	now := d.nowMillis()
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+tableStreamAttempt+` SET state = ?, updated_at = ? WHERE lease_id = ? AND state != ?`,
		attemptTombstoned, now, row.LeaseID, attemptTombstoned); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: tombstone before open")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableStreamAttempt+` (`+attemptColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.AttemptID, row.JobID, row.LeaseID, row.CheckpointID, row.TreeHash, attemptLive, "[]", now, now); err != nil {
		return cascade.Wrapf(cascade.KindConflict, err, "ci: stream: open attempt %s", row.AttemptID)
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: commit attempt")
	}
	return nil
}

// requireCurrent refuses an attempt that is unknown or tombstoned: a late
// result must never change a row or publish an event. A terminal attempt
// is still the lease's current one and may take a further dispatch.
func (d *Dispatcher) requireCurrent(ctx context.Context, attemptID string) error {
	row, ok, err := d.getAttempt(ctx, attemptID)
	if err != nil {
		return err
	}
	if !ok || row.State == attemptTombstoned {
		return cascade.Wrapf(cascade.KindConflict, ErrLateResult, "ci: stream: attempt %s is tombstoned or unknown", attemptID)
	}
	return nil
}

// appendEntry adds e to the attempt's dispatch list unless an entry with
// the same acceptance flag and kinds is already there (a repeated Dispatch
// of the same request records it once).
func (d *Dispatcher) appendEntry(ctx context.Context, attemptID string, e dispatchEntry) error {
	tx, err := d.deps.CIDB.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: begin dispatch record")
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT dispatches FROM `+tableStreamAttempt+` WHERE attempt_id = ?`, attemptID).Scan(&raw); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: read dispatches")
	}
	var entries []dispatchEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "ci: stream: unreadable dispatches")
	}
	for _, have := range entries {
		if have.Acceptance == e.Acceptance && sameKinds(have.Kinds, e.Kinds) {
			return nil
		}
	}
	next, err := json.Marshal(append(entries, e))
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "ci: stream: encode dispatches")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+tableStreamAttempt+` SET dispatches = ?, updated_at = ? WHERE attempt_id = ?`,
		string(next), d.nowMillis(), attemptID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: write dispatches")
	}
	return asKind(tx.Commit(), cascade.KindUnavailable, "ci: stream: commit dispatch record")
}

func sameKinds(a, b []RequirementKind) bool {
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

// setState moves an attempt from one state to another; a row not in the
// from state is left alone.
func (d *Dispatcher) setState(ctx context.Context, attemptID, from, to string) error {
	_, err := d.deps.CIDB.ExecContext(ctx,
		`UPDATE `+tableStreamAttempt+` SET state = ?, updated_at = ? WHERE attempt_id = ? AND state = ?`,
		to, d.nowMillis(), attemptID, from)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: set attempt state")
	}
	return nil
}

// nowMillis is the injected clock's instant in unix milliseconds.
func (d *Dispatcher) nowMillis() int64 { return d.deps.Clock.Now().UnixMilli() }
