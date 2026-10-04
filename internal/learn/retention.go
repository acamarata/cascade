package learn

// Purpose: RetentionSweep, the learn-retention runnable: deletes
//   jobs_telemetry_outcomes rows older than [learn].retention.max_age_days
//   (default 90, hot) plus every registered child's rows (the finding table
//   among them) in the same transaction, and the generic
//   RegisterRetentionTable / RegisterRetentionChild registration points
//   (contract outputs item P2-9) so a later ticket's own table joins the
//   same sweep without this package learning its name at compile time.
// Inputs: an open *sql.DB, an injected runtime.Clock, and a
//   runtime.PathProvider (each run re-reads config.toml so a hot edit
//   applies at the NEXT run, per 08 §3).
// Outputs: rows deleted; a "learn.retention.swept" event published by the
//   caller (registerLearnJobs) with {rows_deleted}, per Record().
// Constraints: no bare time.Now; a config load error fails that run
//   (never silently sweeps with a stale/default MaxAge); a registered
//   table absent from THIS db (a caller that registered but never
//   applied its own migration here) is skipped, not a hard failure --
//   RetentionSweep never learns another package's migration lifecycle;
//   children are deleted before their parents inside ONE transaction and
//   any error rolls the whole batch back; shapes are checked before any
//   delete (retention_registry.go).
// SPORT: internal.learn.RetentionSweep/ADDED,
//   internal.learn.RegisterRetentionTable/ADDED (P1-E31-W6-S64-T1),
//   internal.learn.RegisterRetentionChild/ADDED (P1-CAP-02).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultMaxAgeDays is [learn].retention.max_age_days's shipped default.
const DefaultMaxAgeDays = 90

// RegisterRetentionTable registers table (swept by comparing timeColumn
// against the same cutoff RetentionSweep.Run computes) so a run also
// deletes table's aged-out rows. Both names are checked against
// retentionIdentifierPattern before being held. A name that fails
// validation, a table already registered (as a table or a child), or a
// learn-owned table that has no such column (jobs_telemetry_finding has
// no time column) refuses with KindInvalidInput; a table owned elsewhere
// is shape-checked at its first sweep, before any delete. Registration is
// permanent for the process lifetime.
func RegisterRetentionTable(table, timeColumn string) error {
	return defaultRetention.addTable(table, timeColumn)
}

// RegisterRetentionChild registers table as a dependent keyed by
// jobs_telemetry_outcomes.id through parentIDColumn (jobs_telemetry_finding
// via outcome_id, and every extension table of the additive rule).
// RetentionSweep deletes a registered child's rows for the parent rows it
// retires in the SAME transaction, before the parent delete; a child delete
// error rolls the whole batch back. A malformed identifier, a duplicate, or
// a learn-owned table without parentIDColumn refuses with KindInvalidInput;
// a table owned elsewhere is shape-checked at its first sweep, before any
// delete.
func RegisterRetentionChild(table, parentIDColumn string) error {
	return defaultRetention.addChild(table, parentIDColumn)
}

// learnOutcomesTimeColumn and learnFindingParentColumn are learn's own two
// registrations (contract outputs item P2-9).
const (
	learnOutcomesTimeColumn  = "created_at"
	learnFindingParentColumn = "outcome_id"
)

// registerLearnRetention registers learn's own two tables on reg: the
// outcomes table by created_at, the finding table as its child. init makes
// the same two registrations through the exported functions; this form
// lets a test build a private registry.
func registerLearnRetention(reg *retentionRegistry) error {
	if err := reg.addTable(tableTelemetryOutcomes, learnOutcomesTimeColumn); err != nil {
		return err
	}
	return reg.addChild(tableTelemetryFinding, learnFindingParentColumn)
}

// init registers learn's own tables on the process-wide registry through
// the exported calls a later ticket's table also uses.
func init() {
	if err := RegisterRetentionTable(tableTelemetryOutcomes, learnOutcomesTimeColumn); err != nil {
		panic("learn: register " + tableTelemetryOutcomes + " for retention: " + err.Error())
	}
	if err := RegisterRetentionChild(tableTelemetryFinding, learnFindingParentColumn); err != nil {
		panic("learn: register " + tableTelemetryFinding + " for retention: " + err.Error())
	}
}

// RetentionSweep is the learn-retention runnable's real implementation.
type RetentionSweep struct {
	DB      *sql.DB
	Clock   runtime.Clock
	Paths   runtime.PathProvider
	Getenv  runtime.Getenv
	Environ func() []string

	// registry overrides the process-wide registry; tests set it so a
	// registration made for one test never reaches another's sweep.
	registry *retentionRegistry
}

// Run reads [learn].retention.max_age_days fresh (so a hot config edit
// applies at the next run), checks every registered table's shape, then in
// ONE transaction deletes each registered child's rows for the parents it
// retires and then every registered table's rows older than MaxAge. Any
// error rolls the whole batch back. Returns the total rows deleted; an empty
// table is a no-op (0, nil).
func (r RetentionSweep) Run(ctx context.Context) (rowsDeleted int64, err error) {
	if r.DB == nil || r.Clock == nil || r.Paths == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: RetentionSweep requires DB, Clock, and Paths")
	}
	reg := r.registry
	if reg == nil {
		reg = defaultRetention
	}
	tables, children := reg.snapshot()
	if err := validateShapes(ctx, r.DB, tables, children); err != nil {
		return 0, err
	}
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: r.Paths.ConfigPath(), Getenv: r.Getenv, Environ: r.Environ})
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: retention sweep: load config")
	}
	cutoff := r.Clock.Now().UTC().AddDate(0, 0, -RetentionMaxAgeDays(cfg)).Unix()

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: retention sweep: begin transaction")
	}
	total, err := sweepInTx(ctx, tx, cutoff, tables, children)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: retention sweep: commit")
	}
	return total, nil
}

// sweepInTx deletes the retired parents' children, then the aged rows of
// every registered table, inside tx. A registered table absent from the
// database is skipped.
func sweepInTx(ctx context.Context, tx *sql.Tx, cutoff int64, tables []retentionTableReg, children []retentionChildReg) (int64, error) {
	var total int64
	parentTime, _ := retentionTimeColumn(tables, tableTelemetryOutcomes)
	for _, c := range children {
		exists, err := tableExists(ctx, tx, c.table)
		if err != nil {
			return 0, err
		}
		if !exists {
			continue
		}
		n, err := execDelete(ctx, tx, c.table, `DELETE FROM `+c.table+` WHERE `+c.parentIDColumn+
			` IN (SELECT id FROM `+tableTelemetryOutcomes+` WHERE `+parentTime+` < ?)`, cutoff)
		if err != nil {
			return 0, err
		}
		total += n
	}
	for _, t := range tables {
		exists, err := tableExists(ctx, tx, t.table)
		if err != nil {
			return 0, err
		}
		if !exists {
			continue
		}
		n, err := execDelete(ctx, tx, t.table, `DELETE FROM `+t.table+` WHERE `+t.timeColumn+` < ?`, cutoff)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// execDelete runs one DELETE and returns the rows it removed.
func execDelete(ctx context.Context, tx *sql.Tx, table, query string, cutoff int64) (int64, error) {
	res, err := tx.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "learn: retention sweep: delete from %q", table)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "learn: retention sweep: read rows affected for %q", table)
	}
	return n, nil
}

// RetentionMaxAgeDays reads cfg.Learn.Retention.MaxAgeDays (see
// internal/runtime/config.go's learnSection), falling back to
// DefaultMaxAgeDays for a nil cfg or an unset/invalid (<=0) value --
// Run always has a usable MaxAge, never a zero-day sweep by accident.
func RetentionMaxAgeDays(cfg *runtime.Config) int {
	if cfg == nil || cfg.Learn.Retention.MaxAgeDays <= 0 {
		return DefaultMaxAgeDays
	}
	return cfg.Learn.Retention.MaxAgeDays
}
