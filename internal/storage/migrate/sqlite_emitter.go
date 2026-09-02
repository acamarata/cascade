package migrate

import "github.com/acamarata/cascade/pkg/cascade"

// Purpose: SQLiteEmitter converts a MigrationSet into ordered modernc-
//
//	SQLite DDL statements — CREATE TABLE IF NOT EXISTS with per-column
//	SQLite type affinities, FOREIGN KEY clauses, and CREATE INDEX IF NOT
//	EXISTS. The structural walk (identifier validation, quoting, PK
//	shape) lives in emit_common.go/emit_table.go, shared with
//	PostgresEmitter; this file supplies only what SQLite actually spells
//	differently: its column-type keywords and its
//	"INTEGER PRIMARY KEY AUTOINCREMENT" autoincrement form.
//
// Inputs: a MigrationSet.
// Outputs: []string DDL statements, one per step, or a *cascade.Error.
// Constraints: TypeText->TEXT, TypeInteger->INTEGER, TypeReal->REAL,
//
//	TypeBlob->BLOB (modernc-sqlite's four native type affinities, per
//	02-TARGET-STRUCTURE.md and this ticket's full_desc). No CGO
//	(SQLiteEmitter only builds strings; it never opens a database — see
//	ledger.go/migrate_test.go for the real-counterpart execution against
//	modernc.org/sqlite).
//
// SPORT: internal.storage.migrate.SQLiteEmitter/ADDED (P1-E02-W1-S02-T3).

// SQLiteEmitter emits modernc-SQLite DDL. It carries no state and is safe
// for concurrent use.
type SQLiteEmitter struct{}

// Name returns "sqlite".
func (SQLiteEmitter) Name() string { return "sqlite" }

// Emit renders set into ordered SQLite DDL statements.
func (e SQLiteEmitter) Emit(set MigrationSet) ([]string, error) {
	return emitSet(set, dialectHooks{
		name:                "sqlite",
		columnType:          sqliteColumnType,
		autoincrementColumn: sqliteAutoincrementColumn,
	})
}

// sqliteColumnType maps a ColumnType to its SQLite type-affinity keyword.
func sqliteColumnType(t ColumnType) (string, error) {
	switch t {
	case TypeText:
		return "TEXT", nil
	case TypeInteger:
		return "INTEGER", nil
	case TypeReal:
		return "REAL", nil
	case TypeBlob:
		return "BLOB", nil
	default:
		return "", cascade.Newf(cascade.KindInvalidInput, "migrate: sqlite: unknown ColumnType %d", t)
	}
}

// sqliteAutoincrementColumn renders SQLite's rowid-aliased autoincrement
// primary key form. AUTOINCREMENT in SQLite requires the column to be
// exactly "INTEGER PRIMARY KEY AUTOINCREMENT" — no other type keyword,
// constraint ordering, or additional column-level constraint is accepted
// by SQLite's grammar for this form.
func sqliteAutoincrementColumn(colName string) (string, error) {
	return quoteIdent(colName) + " INTEGER PRIMARY KEY AUTOINCREMENT", nil
}

var _ Dialect = SQLiteEmitter{}
