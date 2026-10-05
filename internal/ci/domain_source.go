// Purpose (this file): the ci_run "source" side table (P1-E25-W5-S51-T5,
// task 3) -- which producer wrote a ci_run row: 'github-actions' (T2's
// polling client) or 'local' (this ticket's runner.go). A NEW table, not
// an ALTER TABLE ... ADD COLUMN on ci_run itself, because
// internal/storage/migrate's DSL is CREATE-only (dsl.go's own doc
// comment: "adding a column ... means authoring a new MigrationStep for
// a new table") -- the exact same gap internal/conversation/archive.go's
// conversation_thread_archive table already worked around for the same
// reason.
//
// Split out of domain.go purely to keep that file under Art.10.3's
// 300-line cap once this ticket's fourth MigrationStep and two new
// functions landed (files_scope names domain.go for this change; this
// sibling file follows the SAME extraction domain.go's own doc comment
// already documents for jobTableStep/stepTableStep's neighbors -- see
// this file's own SPORT line).
//
// Inputs: a run's (run_id, repo_id) key and a source string
// ("github-actions" | "local").
// Outputs: an idempotent upsert of that run's source, and a read-back
// that defaults to "github-actions" for any row this ticket never wrote
// (T2's own writes never touch this table -- see runSource's doc
// comment for why that default is correct, not a guess).
// Constraints: no schema-version bump lives in this file (domain.go
// already carries ciSchemaVersion 9) -- this file only supplies the
// fourth MigrationStep domain.go's MigrationSet references.
// SPORT: internal.ci.UpsertRunSource/ADDED, internal.ci.runSource/ADDED
//
//	(P1-E25-W5-S51-T5).

package ci

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// SourceGitHubActions and SourceLocal are the two producers a ci_run row
// can carry. SourceGitHubActions is also runSource's default for any row
// with no ci_run_source entry -- every row T2's poller ever wrote predates
// this table, and T2's own Upsert is left unmodified (backward
// compatibility, this ticket's own acceptance criterion), so "no entry"
// can only ever mean a GitHub-Actions-sourced row.
const (
	SourceGitHubActions = "github-actions"
	SourceLocal         = "local"
)

// sourceTableStep defines ci_run_source: one row per (run_id, repo_id).
func sourceTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_run_source: which producer (github-actions|local) wrote a ci_run row",
		Table: &migrate.TableDef{
			Name: tableRunSource,
			Columns: []migrate.ColumnDef{
				{Name: "run_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "repo_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "source", Type: migrate.TypeText, NotNull: true},
			},
			// NO foreign key to ci_run, and the earlier claim that this
			// was "foreign-keyed exactly like ci_job" was simply wrong:
			// ci_job declares no foreign key at all. The reference it
			// did declare was single-column, run_id -> ci_run.run_id,
			// while ci_run's primary key is the COMPOSITE (run_id,
			// repo_id) -- so the constraint pointed at a non-unique
			// column and SQLite rejects it the moment PRAGMA
			// foreign_keys is ON. It only ever "worked" because SQLite
			// leaves enforcement off by default, which is a latent
			// break, not a design (internal/conversation/privacy.go's
			// privacyStep records the identical finding for
			// conversation_thread_privacy). migrate's ForeignKeyDef is
			// single-column by construction -- Column and RefColumn are
			// each one validated identifier -- so the composite
			// constraint cannot be expressed through this DSL at all.
			// This is therefore a MARKER table, on the same terms as
			// that one: a row describes a run, its absence is defined as
			// meaningful ("github-actions", see runSource), and a row
			// for a run that is gone is harmless rather than an
			// integrity error.
		},
	}
}

// UpsertRunSource idempotently records which producer wrote (runID,
// repoID)'s ci_run row. Called once per Run, after Upsert has written the
// ci_run row itself -- by convention, not by a constraint (see
// sourceTableStep on why this table carries no foreign key).
func UpsertRunSource(ctx context.Context, db *sql.DB, runID, repoID int64, source string) error {
	if source != SourceGitHubActions && source != SourceLocal {
		return cascade.Newf(cascade.KindInvalidInput, "ci: UpsertRunSource: unrecognised source %q", source)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO `+tableRunSource+` (run_id, repo_id, source)
		VALUES (?, ?, ?)
		ON CONFLICT(run_id, repo_id) DO UPDATE SET source=excluded.source`,
		runID, repoID, source)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: upsert run source %d/%d", runID, repoID)
	}
	return nil
}

// runSource reads back (runID, repoID)'s recorded source, defaulting to
// SourceGitHubActions when no ci_run_source row exists -- see this file's
// doc comment for why that default (never SourceLocal) is the correct
// backward-compatible reading of an absent row. Deliberately UNEXPORTED
// (R-14.283): ListRuns below already serves `cascade ci status`'s
// per-run source need via a SQL JOIN, so this single-row lookup has no
// production caller today and no confirmed future one in S-51.T6/
// S-52.*/E-AJ's S-72.T4 (which builds its own independent live-visibility
// check, not a reader of this table) -- exported-but-uncalled would
// misstate that; unexported-but-tested (same package, this file's own
// _test.go) is the honest shape until a real caller exists.
func runSource(ctx context.Context, db *sql.DB, runID, repoID int64) (string, error) {
	var source string
	row := db.QueryRowContext(ctx, `SELECT source FROM `+tableRunSource+` WHERE run_id=? AND repo_id=?`, runID, repoID)
	err := row.Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceGitHubActions, nil
	}
	if err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "ci: reading run source %d/%d", runID, repoID)
	}
	return source, nil
}

// RunSummary is one row of `cascade ci status`'s combined view: a ci_run
// record joined with its recorded source.
type RunSummary struct {
	RunID      int64
	RepoID     int64
	Name       string
	Status     RunStatus
	Conclusion RunConclusion
	Source     string
	UpdatedAt  time.Time
}

// ListRuns returns the most recently updated runs (both
// source=github-actions and source=local), newest first, bounded by
// limit (a non-positive limit defaults to 20). This is `cascade ci
// status`'s only read path -- it never distinguishes sources at the SQL
// level, matching this ticket's acceptance criterion that status shows
// both.
func ListRuns(ctx context.Context, db *sql.DB, limit int) ([]RunSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `
		SELECT r.run_id, r.repo_id, r.name, r.status, r.conclusion, r.updated_at, COALESCE(s.source, ?)
		FROM `+tableRun+` r
		LEFT JOIN `+tableRunSource+` s ON s.run_id = r.run_id AND s.repo_id = r.repo_id
		ORDER BY r.updated_at DESC
		LIMIT ?`, SourceGitHubActions, limit)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: listing runs")
	}
	defer func() { _ = rows.Close() }()

	var out []RunSummary
	for rows.Next() {
		var s RunSummary
		var status, conclusion string
		var updatedMillis int64
		if err := rows.Scan(&s.RunID, &s.RepoID, &s.Name, &status, &conclusion, &updatedMillis, &s.Source); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: scanning a run row")
		}
		s.Status, s.Conclusion = RunStatus(status), RunConclusion(conclusion)
		s.UpdatedAt = time.UnixMilli(updatedMillis)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: iterating run rows")
	}
	return out, nil
}

// runStreamTableStep defines ci_run_stream: one marker row per run that a
// streaming checkpoint dispatched (P1-CI-01). A new table, not a column on
// ci_run_source, for the same reason domain_source.go's own header gives
// (the migrate DSL is CREATE-only). An absent row means via_stream=false.
func runStreamTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_run_stream: runs dispatched by a streaming checkpoint, with their sensitivity tier and checkpoint id",
		Table: &migrate.TableDef{
			Name: tableRunStream,
			Columns: []migrate.ColumnDef{
				{Name: "run_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "repo_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "via_stream", Type: migrate.TypeInteger, NotNull: true},
				{Name: "sensitivity", Type: migrate.TypeText, NotNull: true},
				{Name: "checkpoint_id", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

// UpsertRunSourceStream records, in ONE transaction, which producer wrote
// (runID, repoID)'s ci_run row (exactly UpsertRunSource's ci_run_source
// write) and the stream marker: whether a streaming checkpoint dispatched
// it, the data tier that travelled with the job and the checkpoint id. A
// tier outside the closed set refuses, so a corrupt value is never stored
// as if it were a real classification.
func UpsertRunSourceStream(ctx context.Context, db *sql.DB, runID, repoID int64, source string, viaStream bool,
	sensitivity provider.SensitivityTier, checkpointID string) error {
	if source != SourceGitHubActions && source != SourceLocal {
		return cascade.Newf(cascade.KindInvalidInput, "ci: UpsertRunSourceStream: unrecognised source %q", source)
	}
	if !sensitivity.Valid() {
		return cascade.New(cascade.KindInvalidInput, "ci: UpsertRunSourceStream: invalid sensitivity tier")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: begin run source %d/%d", runID, repoID)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO `+tableRunSource+` (run_id, repo_id, source)
		VALUES (?, ?, ?)
		ON CONFLICT(run_id, repo_id) DO UPDATE SET source=excluded.source`,
		runID, repoID, source); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: upsert run source %d/%d", runID, repoID)
	}
	via := 0
	if viaStream {
		via = 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO `+tableRunStream+` (run_id, repo_id, via_stream, sensitivity, checkpoint_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(run_id, repo_id) DO UPDATE SET
			via_stream=excluded.via_stream, sensitivity=excluded.sensitivity, checkpoint_id=excluded.checkpoint_id`,
		runID, repoID, via, sensitivity.String(), checkpointID); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: upsert run stream %d/%d", runID, repoID)
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: commit run source %d/%d", runID, repoID)
	}
	return nil
}

// RunViaStream reports whether (runID, repoID) was dispatched by a
// streaming checkpoint. An absent ci_run_stream row, which every run
// UpsertRunSource wrote and every GitHub Actions run, reads as false.
func RunViaStream(ctx context.Context, db *sql.DB, runID, repoID int64) (bool, error) {
	var via sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT s.via_stream FROM `+tableRunSource+` r
		LEFT JOIN `+tableRunStream+` s ON s.run_id = r.run_id AND s.repo_id = r.repo_id
		WHERE r.run_id = ? AND r.repo_id = ?`, runID, repoID).Scan(&via)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "ci: reading run stream flag %d/%d", runID, repoID)
	}
	return via.Valid && via.Int64 == 1, nil
}
