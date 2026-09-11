package migrate

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the raw SQL query/exec helpers ledger.go's Apply orchestrates —
//
//	split out under R-14.117 (Art.10.3's 300-line file cap) as a sibling
//	file in the same package, behaviour-preserving, no signature changes
//	to anything ledger.go exports.
//
// SPORT: internal.storage.migrate.Ledger/ADDED (P1-E02-W1-S02-T3).

// ledgerRow is one row of the applied_migrations table, in the order
// ledgerRowsForVersion returns them (ORDER BY id, i.e. application order).
type ledgerRow struct {
	schemaVersion int
	checksum      string
	appliedAt     int64
}

// ensureLedgerTable creates the applied_migrations table if it does not
// already exist, via dialect's own emitter — so the ledger table gets
// exactly the same dialect-correct DDL as any caller-authored table (see
// ledgerDef in ledger.go). This is infrastructure bootstrap, not a
// tracked migration step: it is never recorded as a ledger row itself.
func ensureLedgerTable(ctx context.Context, db *sql.DB, dialect Dialect) error {
	stmts, err := dialect.Emit(MigrationSet{
		Steps: []MigrationStep{{Kind: StepCreateTable, Table: &ledgerDef, ledgerBootstrap: true}},
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "migrate: emit ledger bootstrap DDL")
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "migrate: create "+ledgerTableName+" table")
		}
	}
	return ensureSetIDColumn(ctx, db)
}

// ensureSetIDColumn upgrades an on-disk applied_migrations table that
// predates R-16.77's per-set identity column: ledgerDef's set_id column
// only lands on a fresh CREATE TABLE (ensureLedgerTable's IF NOT EXISTS
// is a no-op against an existing table), so an existing table needs this
// explicit ALTER. Guarded so it is idempotent: a database that already
// has the column returns the driver's "already exists"/"duplicate
// column" error, which this treats as success rather than surfacing it.
// Legacy rows get the empty-string default (R-16.77), which is invisible
// to every per-set query (they all filter WHERE set_id = ?), so each
// set's next Apply simply re-runs its own steps against the CREATE TABLE/
// INDEX IF NOT EXISTS statements ledger.go's own guarantee already says
// are idempotent no-ops.
func ensureSetIDColumn(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		`ALTER TABLE `+quoteIdent(ledgerTableName)+` ADD COLUMN set_id TEXT NOT NULL DEFAULT ''`)
	if err == nil || isDuplicateColumnError(err) {
		return nil
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "migrate: add set_id column to "+ledgerTableName)
}

// isDuplicateColumnError reports whether err is the driver's "this column
// already exists" refusal — both modernc-sqlite ("duplicate column name:
// set_id") and Postgres ("column \"set_id\" ... already exists") report
// this as a plain error with no typed sentinel, so string matching is the
// only real-counterpart signal either driver gives.
func isDuplicateColumnError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

// currentSchemaVersion returns MAX(schema_version) recorded in the
// ledger for setID, or 0 if that set has no rows yet (a fresh set, or a
// fresh database). Scoped per R-16.77: a legacy row (set_id = "") or a
// different set's rows never affect this result.
func currentSchemaVersion(ctx context.Context, db *sql.DB, dialect Dialect, setID string) (int, error) {
	var version sql.NullInt64
	row := db.QueryRowContext(ctx,
		`SELECT MAX(schema_version) FROM `+quoteIdent(ledgerTableName)+` WHERE set_id = `+paramPlaceholder(dialect, 1),
		setID)
	if err := row.Scan(&version); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "migrate: read current schema_version")
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// paramPlaceholder returns the dialect-correct bound-parameter marker for
// the i'th (1-indexed) parameter in a query: SQLite (and every other
// database/sql driver that speaks the ordinal "?" convention) uses a bare
// "?" regardless of position; Postgres's extended query protocol requires
// the positional "$1", "$2", ... form instead. Every hand-written query in
// this file that carries a bound parameter goes through this helper — a
// literal "?" hard-coded into a query string is exactly the bug this
// function exists to prevent (P1-E17-W4-S38-T4 found ledgerRowsForVersion
// and insertLedgerRow shipping a bare "?" that had never been exercised
// against a live Postgres server before this ticket's docker lane;
// Postgres's own driver rejects "?" outright as a syntax error rather than
// silently accepting it, which is why the SQLite-only lane never caught
// this).
func paramPlaceholder(dialect Dialect, i int) string {
	if dialect != nil && dialect.Name() == "postgres" {
		return "$" + strconv.Itoa(i)
	}
	return "?"
}

// ledgerRowsForVersion returns every ledger row recorded for setID at
// schemaVersion, in application order (ORDER BY id). Scoped by set_id
// (R-16.77) so a different set's rows at the same schemaVersion are
// never returned here.
func ledgerRowsForVersion(ctx context.Context, db *sql.DB, dialect Dialect, setID string, schemaVersion int) ([]ledgerRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT schema_version, checksum, applied_at FROM `+quoteIdent(ledgerTableName)+
			` WHERE schema_version = `+paramPlaceholder(dialect, 1)+` AND set_id = `+paramPlaceholder(dialect, 2)+` ORDER BY id`,
		schemaVersion, setID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "migrate: read ledger rows")
	}
	defer func() { _ = rows.Close() }()

	var out []ledgerRow
	for rows.Next() {
		var r ledgerRow
		if err := rows.Scan(&r.schemaVersion, &r.checksum, &r.appliedAt); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "migrate: scan ledger row")
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "migrate: iterate ledger rows")
	}
	return out, nil
}

// insertLedgerRow records one newly-applied step under setID.
func insertLedgerRow(ctx context.Context, db *sql.DB, dialect Dialect, setID string, schemaVersion int, checksum string, appliedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(ledgerTableName)+` (schema_version, checksum, applied_at, set_id) VALUES (`+
			paramPlaceholder(dialect, 1)+`, `+paramPlaceholder(dialect, 2)+`, `+paramPlaceholder(dialect, 3)+`, `+paramPlaceholder(dialect, 4)+`)`,
		schemaVersion, checksum, appliedAt.Unix(), setID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "migrate: insert ledger row")
	}
	return nil
}
