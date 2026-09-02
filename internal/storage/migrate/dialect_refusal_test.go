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
