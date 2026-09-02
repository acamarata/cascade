package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mcsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: small raw-SQL helpers shared by domains.go (Bootstrap) and
//
//	health.go (StorageHealthCheck) — sqlite_master presence queries,
//	identifier quoting, anchor-table creation, and the applied_migrations
//	stamp row. Split out as a sibling file from the start (not a later
//	R-14.117 remedy) to keep domains.go and health.go each well under
//	Art.10.3's 300-line cap while both need the same primitives.
//
// Inputs/Outputs: see each function's own doc comment.
// Constraints: every exec/query here operates on a real modernc-sqlite
//
//	*sql.DB (Art.2) — never a self-authored schema double. Table names
//	passed in always originate from this package's own AllDomains
//	TablePrefix values or fixed constants, never external input, but
//	quoteIdent is still applied everywhere a name is interpolated into
//	SQL text, as defense in depth and to match
//	internal/storage/migrate's own identifier-quoting discipline.
//
// SPORT: internal.storage.domains.Bootstrap/ADDED,
//
//	internal.storage.health.StorageHealthCheck/ADDED (P1-E02-W1-S03-T1).

// quoteIdent wraps name in double quotes, doubling any embedded quote —
// standard SQL identifier quoting, mirrored from
// internal/storage/migrate's own (unexported, unimported here per the
// ticket's "no migrate code consumed" constraint) helper of the same
// name and behaviour.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// tableExists reports whether table is present in sqlite_master — the
// Art.2 real-counterpart check every caller in this package uses instead
// of tracking presence in memory, so a database mutated out-of-band
// between calls is always reported accurately.
func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
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

// createAnchorTable idempotently creates table as a minimal anchor
// (id INTEGER PRIMARY KEY) if it does not already exist, and reports
// whether THIS call was the one that created it (computed from a
// sqlite_master presence check taken before the CREATE, since "CREATE
// TABLE IF NOT EXISTS" itself gives no such signal back to the caller).
func createAnchorTable(ctx context.Context, db *sql.DB, table string) (created bool, err error) {
	existed, err := tableExists(ctx, db, table)
	if err != nil {
		return false, err
	}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY)`, quoteIdent(table))
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return false, err
	}
	return !existed, nil
}

// ensureLedgerTable creates the applied_migrations ledger table if it does
// not already exist, in the shape shared by spec with
// internal/storage/migrate's ledgerDef (id, schema_version, checksum,
// applied_at) — see domains.go's package doc for why this is a shape-only
// agreement, never a code import.
func ensureLedgerTable(ctx context.Context, db *sql.DB) error {
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		schema_version INTEGER NOT NULL,
		checksum TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`, quoteIdent(bootstrapLedgerTable))
	_, err := db.ExecContext(ctx, ddl)
	return err
}

// stampSchemaVersion ensures the ledger table exists and idempotently
// inserts the bootstrapSchemaVersion stamp row: if a row for
// bootstrapSchemaVersion is already present (a re-run of Bootstrap, or a
// database some other process already stamped), no INSERT happens and no
// existing row is mutated — the §5.9 idempotency contract applied to the
// stamp specifically, not only to the anchor tables.
func stampSchemaVersion(ctx context.Context, db *sql.DB, clock Clock) error {
	if err := ensureLedgerTable(ctx, db); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "storage: bootstrap ledger table")
	}

	var alreadyStamped bool
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM `+quoteIdent(bootstrapLedgerTable)+` WHERE schema_version = ? LIMIT 1`,
		bootstrapSchemaVersion,
	).Scan(new(int))
	switch err {
	case nil:
		alreadyStamped = true
	case sql.ErrNoRows:
		alreadyStamped = false
	default:
		return cascade.Wrap(cascade.KindUnavailable, err, "storage: read schema_version stamp")
	}
	if alreadyStamped {
		return nil
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(bootstrapLedgerTable)+` (schema_version, checksum, applied_at) VALUES (?, ?, ?)`,
		bootstrapSchemaVersion, bootstrapStampChecksum, clock.Now().Unix(),
	)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "storage: insert schema_version stamp row")
	}
	return nil
}

// classifyProbeError reports the taxonomy Kind that best fits a raw
// database/sql error from health_probe.go's insert/delete round-trip, by
// inspecting the real SQLite result code (never the error string — see
// providers/sqlite/errors.go's classifyDBError, which this mirrors in
// miniature for the one case health_probe.go needs: a read-only database
// file must report KindPermissionDenied, not the generic KindUnavailable
// every other unrecognized code falls back to).
func classifyProbeError(err error) cascade.Kind {
	var sqliteErr *mcsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return cascade.KindUnavailable
	}
	switch sqliteErr.Code() & 0xff { // mask off any extended-result-code bits
	case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_PERM, sqlite3.SQLITE_AUTH:
		return cascade.KindPermissionDenied
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return cascade.KindIntegrity
	default:
		return cascade.KindUnavailable
	}
}
