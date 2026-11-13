// Package docket provides schema migrations for the ticket ledger.
package docket

import (
	"database/sql"
	"errors"
)

// ErrLedgerMigration is returned when the ledger migration fails.
var ErrLedgerMigration = errors.New("ERR_LEDGER_MIGRATION")

// ApplyLedgerMigration creates the ticket_ledger, owner_item and assumption
// tables inside a single transaction. A second call is a safe no-op.
func ApplyLedgerMigration(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return ErrLedgerMigration
	}
	defer tx.Rollback()

	// Check which tables already exist so the migration is idempotent.
	existing := map[string]bool{}
	rows, err := tx.Query("SELECT name FROM sqlite_master WHERE type='table' AND name IN ('ticket_ledger','owner_item','assumption')")
	if err != nil {
		return ErrLedgerMigration
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return ErrLedgerMigration
		}
		existing[n] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ErrLedgerMigration
	}
	rows.Close()

	stmts := map[string]string{
		"ticket_ledger": `CREATE TABLE ticket_ledger (id INTEGER PRIMARY KEY)`,
		"owner_item":    `CREATE TABLE owner_item (id INTEGER PRIMARY KEY)`,
		"assumption":    `CREATE TABLE assumption (id INTEGER PRIMARY KEY)`,
	}

	for tbl, stmt := range stmts {
		if !existing[tbl] {
			if _, err := tx.Exec(stmt); err != nil {
				return ErrLedgerMigration
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return ErrLedgerMigration
	}
	return nil
}
