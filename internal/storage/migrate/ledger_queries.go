package migrate

import (
	"context"
	"database/sql"
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
		Steps: []MigrationStep{{Kind: StepCreateTable, Table: &ledgerDef}},
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "migrate: emit ledger bootstrap DDL")
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "migrate: create "+ledgerTableName+" table")
		}
	}
	return nil
}

// currentSchemaVersion returns MAX(schema_version) recorded in the
// ledger, or 0 if the ledger has no rows yet (a fresh database).
func currentSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	row := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM `+quoteIdent(ledgerTableName))
	if err := row.Scan(&version); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "migrate: read current schema_version")
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// ledgerRowsForVersion returns every ledger row recorded for
// schemaVersion, in application order (ORDER BY id).
func ledgerRowsForVersion(ctx context.Context, db *sql.DB, schemaVersion int) ([]ledgerRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT schema_version, checksum, applied_at FROM `+quoteIdent(ledgerTableName)+` WHERE schema_version = ? ORDER BY id`,
		schemaVersion)
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

// insertLedgerRow records one newly-applied step.
func insertLedgerRow(ctx context.Context, db *sql.DB, schemaVersion int, checksum string, appliedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(ledgerTableName)+` (schema_version, checksum, applied_at) VALUES (?, ?, ?)`,
		schemaVersion, checksum, appliedAt.Unix())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "migrate: insert ledger row")
	}
	return nil
}
