package migrate

import "github.com/acamarata/cascade/pkg/cascade"

// Purpose: PostgresEmitter converts a MigrationSet into ordered standard-
//
//	wire Postgres DDL statements — CREATE TABLE IF NOT EXISTS with
//	Postgres real types, FOREIGN KEY clauses, and CREATE INDEX IF NOT
//	EXISTS. Shares the structural walk in emit_common.go/emit_table.go
//	with SQLiteEmitter; this file supplies only Postgres's own type
//	keywords and its SERIAL autoincrement form.
//
// Inputs: a MigrationSet.
// Outputs: []string DDL statements, one per step, or a *cascade.Error.
// Constraints: TypeText->TEXT, TypeInteger->BIGINT, TypeReal->
//
//	DOUBLE PRECISION, TypeBlob->BYTEA, matching this ticket's full_desc
//	mapping table exactly. An autoincrement TypeInteger primary key emits
//	SERIAL (32-bit, matching the ticket's literal "SERIAL for
//	autoincrement PKs" — not BIGSERIAL) instead of BIGINT, since SERIAL
//	already implies "integer primary key with an owned sequence."
//	Verified against golden fixtures captured from a real Postgres
//	instance (docker) — see testdata/README.md for provenance and the
//	honest split between golden-diff (this package's automated go test
//	suite) and live-execution proof (performed manually via docker for
//	this ticket, documented in the ticket journal — CI here has no
//	Postgres available, so it is not part of the automated `go test`
//	gate).
//
// SPORT: internal.storage.migrate.PostgresEmitter/ADDED (P1-E02-W1-S02-T3).

// PostgresEmitter emits standard-wire Postgres DDL. It carries no state
// and is safe for concurrent use.
type PostgresEmitter struct{}

// Name returns "postgres".
func (PostgresEmitter) Name() string { return "postgres" }

// Emit renders set into ordered Postgres DDL statements.
func (e PostgresEmitter) Emit(set MigrationSet) ([]string, error) {
	return emitSet(set, dialectHooks{
		name:                "postgres",
		columnType:          postgresColumnType,
		autoincrementColumn: postgresAutoincrementColumn,
	})
}

// postgresColumnType maps a ColumnType to its Postgres type keyword.
func postgresColumnType(t ColumnType) (string, error) {
	switch t {
	case TypeText:
		return "TEXT", nil
	case TypeInteger:
		return "BIGINT", nil
	case TypeReal:
		return "DOUBLE PRECISION", nil
	case TypeBlob:
		return "BYTEA", nil
	default:
		return "", cascade.Newf(cascade.KindInvalidInput, "migrate: postgres: unknown ColumnType %d", t)
	}
}

// postgresAutoincrementColumn renders Postgres's SERIAL autoincrement
// primary key form.
func postgresAutoincrementColumn(colName string) (string, error) {
	return quoteIdent(colName) + " SERIAL PRIMARY KEY", nil
}

var _ Dialect = PostgresEmitter{}
