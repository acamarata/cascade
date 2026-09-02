package storage

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: raw-SQL helpers shared by export.go (Export) and
//
//	export_import.go (Import) — the kv-table shape duplication, its
//	idempotent creation, existence/row-collision probes, and the
//	target-schema_version read both files need. Split out from the start
//	(not a later R-14.117 remedy) so export.go and export_import.go each
//	stay comfortably under Art.10.3's 300-line cap while sharing one
//	definition of the kv table's shape.
//
// Constraints: every function here operates on a real modernc-sqlite
//
//	*sql.DB/*sql.Tx (Art.2) via queryRowCtx/execCtx, the minimal
//	interfaces both types satisfy, so the same helper serves Export's
//	read-only tx and Import's pre-write-tx checks and write-tx inserts
//	without three near-duplicate copies.
//
// SPORT: internal.storage.export.Export/ADDED,
//
//	internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

// exportKVTable duplicates providers/sqlite/driver.go's private schemaDDL
// table name by shape-only convention — see export.go's package doc
// "What Export actually reads" for why this ticket cannot import that
// package's unexported schema instead (files_scope forbids editing
// providers/sqlite, and the table shape is not exported there either).
const exportKVTable = "kv"

// exportKVSchemaDDL mirrors driver.go's schemaDDL exactly (column names,
// types, and the WITHOUT ROWID primary key on (namespace, key)) so a
// database Import writes into — even one Bootstrap alone has touched,
// which creates no kv table at all (domains.go's anchor tables carry no
// business columns) — ends up bit-for-bit schema-compatible with one a
// real providers/sqlite.Open call would have produced.
const exportKVSchemaDDL = `CREATE TABLE IF NOT EXISTS kv (
	namespace TEXT NOT NULL,
	key       TEXT NOT NULL,
	value     BLOB NOT NULL,
	PRIMARY KEY (namespace, key)
) WITHOUT ROWID;`

// queryRowCtx is the minimal read seam both *sql.DB and *sql.Tx satisfy,
// letting kvTableExists/rowExists/readSchemaVersion serve Export's
// read-only transaction and Import's pre-transaction checks alike.
type queryRowCtx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execCtx is the minimal write seam *sql.Tx satisfies, used by Import's
// insert/update helpers (export_import.go). A distinct interface from
// queryRowCtx (rather than one interface with both methods) because
// Export only ever needs the read half.
type execCtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// kvTableExists reports whether the shared kv table exists yet. A domain
// that has never had a key written to it (or a database Bootstrap alone
// has touched) has no kv table at all — Export treats that as "zero rows,"
// never an error; see kvTableExists's callers.
func kvTableExists(ctx context.Context, q queryRowCtx) (bool, error) {
	var name string
	err := q.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, exportKVTable,
	).Scan(&name)
	switch err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}

// ensureKVTable idempotently creates the kv table if absent (Import's
// write path: a target database Import writes into may never have had a
// single Put call route through a real providers/sqlite.Driver).
func ensureKVTable(ctx context.Context, exec execCtx) error {
	_, err := exec.ExecContext(ctx, exportKVSchemaDDL)
	return err
}

// rowExists reports whether (domain, key) already has a value in the kv
// table — the collision probe every ConflictStrategy branch needs before
// deciding insert/update/skip/refuse (export_import.go's applyRow).
func rowExists(ctx context.Context, q queryRowCtx, domain DomainID, key string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(exportKVTable)+` WHERE namespace = ? AND key = ? LIMIT 1`,
		string(domain), key,
	).Scan(&one)
	switch err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}

// readSchemaVersion reads MAX(schema_version) from the applied_migrations
// ledger (domains.go's bootstrapLedgerTable/stampSchemaVersion; the same
// table health.go's checkSchemaVersion reads). Returns a KindIntegrity
// error if the ledger table is missing entirely (an un-bootstrapped
// database) — Export and Import both require a bootstrapped target, per
// this ticket's dependency on B/S-03.T1's Bootstrap.
func readSchemaVersion(ctx context.Context, q queryRowCtx) (int, error) {
	exists, err := kvLedgerExists(ctx, q)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "storage: check applied_migrations presence")
	}
	if !exists {
		return 0, cascade.New(cascade.KindIntegrity,
			"storage: export/import requires a bootstrapped database (applied_migrations table missing)")
	}

	var version sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM `+quoteIdent(bootstrapLedgerTable)).Scan(&version)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "storage: read schema_version stamp")
	}
	if !version.Valid {
		return 0, cascade.New(cascade.KindIntegrity, "storage: applied_migrations table has no schema_version row")
	}
	return int(version.Int64), nil
}

// kvLedgerExists mirrors sqlhelpers.go's tableExists (which is typed to
// *sql.DB specifically) generalized over queryRowCtx, so readSchemaVersion
// serves both Export's *sql.Tx and Import's pre-transaction *sql.DB check.
func kvLedgerExists(ctx context.Context, q queryRowCtx) (bool, error) {
	var name string
	err := q.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, bootstrapLedgerTable,
	).Scan(&name)
	switch err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}
