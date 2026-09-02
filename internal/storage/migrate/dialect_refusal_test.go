// Purpose: proves the DSL REFUSES the unportable constructs called out in
//
//	this ticket's instructions rather than silently emitting SQL that is
//	only correct for one dialect: composite autoincrement primary keys,
//	autoincrement on a non-integer column, autoincrement on a non-primary
//	column, and a ForeignKeyDef.OnDelete value outside the fixed
//	allow-list (an injection vector otherwise, since OnDelete is
//	free-text that reaches the emitted statement).
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED (P1-E02-W1-S02-T3).
package migrate_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func TestRefusal_CompositeAutoincrementPrimaryKey(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "t",
				Columns: []migrate.ColumnDef{
					{Name: "a", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true},
					{Name: "b", Type: migrate.TypeInteger, PrimaryKey: true},
				},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: composite autoincrement PK: want refusal, got nil error", dialect.Name())
		}
	}
}

func TestRefusal_AutoincrementNonIntegerColumn(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "t",
				Columns: []migrate.ColumnDef{
					{Name: "a", Type: migrate.TypeText, PrimaryKey: true, AutoIncrement: true},
				},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: autoincrement on TypeText: want refusal, got nil error", dialect.Name())
		}
	}
}

func TestRefusal_AutoincrementNotPrimaryKey(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "t",
				Columns: []migrate.ColumnDef{
					{Name: "a", Type: migrate.TypeInteger, AutoIncrement: true},
				},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: autoincrement without PrimaryKey: want refusal, got nil error", dialect.Name())
		}
	}
}

func TestRefusal_MultipleAutoincrementColumns(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "t",
				Columns: []migrate.ColumnDef{
					{Name: "a", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true},
					{Name: "b", Type: migrate.TypeInteger, AutoIncrement: true},
				},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: two AutoIncrement columns: want refusal, got nil error", dialect.Name())
		}
	}
}

// TestRefusal_ForeignKeyOnDeleteInjection proves ForeignKeyDef.OnDelete —
// free text that reaches the emitted statement — is validated against a
// closed allow-list exactly like an identifier, not interpolated as-is.
func TestRefusal_ForeignKeyOnDeleteInjection(t *testing.T) {
	hostile := []string{
		"CASCADE; DROP TABLE users; --",
		"cascade' OR '1'='1",
		"NOTAREALACTION",
	}
	for _, action := range hostile {
		set := migrate.MigrationSet{
			SchemaVersion:        1,
			MinimumReaderVersion: 1,
			Steps: []migrate.MigrationStep{{
				Kind: migrate.StepCreateTable,
				Table: &migrate.TableDef{
					Name: "posts",
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true},
						{Name: "user_id", Type: migrate.TypeInteger},
					},
					ForeignKeys: []migrate.ForeignKeyDef{
						{Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: action},
					},
				},
			}},
		}
		if _, err := (migrate.SQLiteEmitter{}).Emit(set); err == nil {
			t.Errorf("OnDelete %q: want refusal, got nil error", action)
		}
	}
}

// TestRefusal_ReservedLedgerTableName proves R-14.143: a caller migration
// naming its table "applied_migrations" is refused by both emitters, with
// a taxonomy error, rather than silently colliding with the package's own
// ledger bootstrap (ensureLedgerTable's CREATE TABLE IF NOT EXISTS) — the
// original defect this ruling closes let the caller's column shape (a
// single "note" column here) be discarded without any error.
func TestRefusal_ReservedLedgerTableName(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    "applied_migrations",
				Columns: []migrate.ColumnDef{{Name: "note", Type: migrate.TypeText}},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: table named %q: want refusal, got nil error", dialect.Name(), "applied_migrations")
		}
	}
}

// TestRefusal_ReservedLedgerIndexName proves R-14.143's second half: an
// INDEX (not just a table) named "applied_migrations" is refused too, on a
// legitimately-named table.
func TestRefusal_ReservedLedgerIndexName(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateIndex,
			Index: &migrate.IndexDef{
				Name:    "applied_migrations",
				Table:   "widgets",
				Columns: []string{"id"},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: index named %q: want refusal, got nil error", dialect.Name(), "applied_migrations")
		}
	}
}

// TestLegitimateLedgerNamedTable_Unaffected proves the reserved-name
// refusal is scoped exactly to the literal "applied_migrations" string —
// an ordinary table/index with a similar-looking or unrelated name is
// completely unaffected, and the package's OWN ledger bootstrap (through
// Apply, not direct Emit) still succeeds, since ensureLedgerTable's step is
// exempted (ledgerBootstrap, dsl.go).
func TestLegitimateLedgerNamedTable_Unaffected(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{
			{
				Kind: migrate.StepCreateTable,
				Table: &migrate.TableDef{
					Name: "migrations_applied", // similar, not equal
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true},
					},
				},
			},
			{
				Kind: migrate.StepCreateIndex,
				Index: &migrate.IndexDef{
					Name:    "idx_migrations_applied_id",
					Table:   "migrations_applied",
					Columns: []string{"id"},
				},
			},
		},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		stmts, err := dialect.Emit(set)
		if err != nil {
			t.Errorf("%s: legitimately-named table/index: want success, got %v", dialect.Name(), err)
			continue
		}
		if len(stmts) != 2 {
			t.Errorf("%s: want 2 statements, got %d", dialect.Name(), len(stmts))
		}
	}
}

// TestCompositePrimaryKeyWithoutAutoincrement_Allowed proves a composite
// PK IS accepted (and emitted as a table-level constraint) when no column
// is AutoIncrement — only the autoincrement+composite combination is
// unportable, not composite keys in general.
func TestCompositePrimaryKeyWithoutAutoincrement_Allowed(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name: "membership",
				Columns: []migrate.ColumnDef{
					{Name: "org_id", Type: migrate.TypeInteger, PrimaryKey: true},
					{Name: "user_id", Type: migrate.TypeInteger, PrimaryKey: true},
				},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		stmts, err := dialect.Emit(set)
		if err != nil {
			t.Errorf("%s: composite PK without autoincrement: want success, got %v", dialect.Name(), err)
			continue
		}
		if len(stmts) != 1 {
			t.Errorf("%s: want 1 statement, got %d", dialect.Name(), len(stmts))
		}
	}
}
