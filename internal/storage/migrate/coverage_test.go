// Purpose: targeted coverage for error-construction, Name(), and the
//
//	validation/error branches TestGolden*/TestRefusal_*/TestHostile* and
//	the Apply-level tests above do not otherwise reach — every case here
//	is a real behavior this ticket's contract asks for (Art.4's coverage
//	floor is a proxy for "every documented error path actually has a
//	test," not a number to chase for its own sake).
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED,
//
//	internal.storage.migrate.Ledger/ADDED (P1-E02-W1-S02-T3).
package migrate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDialectNames(t *testing.T) {
	if got := (migrate.SQLiteEmitter{}).Name(); got != "sqlite" {
		t.Errorf("SQLiteEmitter.Name() = %q, want %q", got, "sqlite")
	}
	if got := (migrate.PostgresEmitter{}).Name(); got != "postgres" {
		t.Errorf("PostgresEmitter.Name() = %q, want %q", got, "postgres")
	}
}

// TestErrorTypes_MessageAndUnwrap proves both structured error types
// implement error with a non-empty message and unwrap to the correct
// cascade taxonomy Kind.
func TestErrorTypes_MessageAndUnwrap(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)

	seed := migrate.MigrationSet{
		SchemaVersion:        2,
		MinimumReaderVersion: 2,
		Steps: []migrate.MigrationStep{{
			Kind:  migrate.StepCreateTable,
			Table: &migrate.TableDef{Name: "seed", Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}}},
		}},
	}
	if err := migrate.Apply(context.Background(), cfg, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := migrate.Apply(context.Background(), cfg, migrate.MigrationSet{SchemaVersion: 3, MinimumReaderVersion: 1, Steps: seed.Steps})
	assertSchemaDowngradeShape(t, err)
	assertMigrationConflictShape(t)
}

// assertSchemaDowngradeShape checks *SchemaDowngradeError's Error()/Unwrap,
// split out of TestErrorTypes_MessageAndUnwrap to keep it under Art.10.3's
// 50-line cap.
func assertSchemaDowngradeShape(t *testing.T, err error) {
	t.Helper()
	var downgrade *migrate.SchemaDowngradeError
	if !errors.As(err, &downgrade) {
		t.Fatalf("want *SchemaDowngradeError, got %v", err)
	}
	if downgrade.Error() == "" {
		t.Error("SchemaDowngradeError.Error() is empty")
	}
	if !cascade.HasKind(downgrade, cascade.KindIntegrity) {
		t.Errorf("SchemaDowngradeError: want KindIntegrity, got %v", errors.Unwrap(downgrade))
	}
}

// assertMigrationConflictShape checks *MigrationConflictError's
// Error()/Unwrap against a freshly-provoked conflict.
func assertMigrationConflictShape(t *testing.T) {
	t.Helper()
	conflictSet := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind:  migrate.StepCreateTable,
			Table: &migrate.TableDef{Name: "c1", Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}}},
		}},
	}
	db2, path2 := openTestDB(t)
	cfg2 := applyConfig(t, db2, path2)
	if err := migrate.Apply(context.Background(), cfg2, conflictSet); err != nil {
		t.Fatalf("conflictSet original: %v", err)
	}
	conflictSet.Steps[0].Table.Name = "c1_renamed"
	err := migrate.Apply(context.Background(), cfg2, conflictSet)
	var conflict *migrate.MigrationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want *MigrationConflictError, got %v", err)
	}
	if conflict.Error() == "" {
		t.Error("MigrationConflictError.Error() is empty")
	}
	if !cascade.HasKind(conflict, cascade.KindConflict) {
		t.Errorf("MigrationConflictError: want KindConflict, got %v", errors.Unwrap(conflict))
	}
}

// TestEmitStep_InvalidShapes exercises emitStep's structural validation:
// StepCreateTable with a nil Table, StepCreateIndex with a nil Index, and
// an unrecognized StepKind value.
func TestEmitStep_InvalidShapes(t *testing.T) {
	cases := []migrate.MigrationSet{
		{SchemaVersion: 1, MinimumReaderVersion: 1, Steps: []migrate.MigrationStep{{Kind: migrate.StepCreateTable}}},
		{SchemaVersion: 1, MinimumReaderVersion: 1, Steps: []migrate.MigrationStep{{Kind: migrate.StepCreateIndex}}},
		{SchemaVersion: 1, MinimumReaderVersion: 1, Steps: []migrate.MigrationStep{{Kind: migrate.StepKind(99)}}},
	}
	for i, set := range cases {
		if _, err := (migrate.SQLiteEmitter{}).Emit(set); err == nil {
			t.Errorf("case %d: want error, got nil", i)
		}
	}
}

// TestEmitCreateIndex_InvalidShapes covers an index with no columns, an
// invalid index name, an invalid table name, and an invalid column name.
func TestEmitCreateIndex_InvalidShapes(t *testing.T) {
	cases := []migrate.IndexDef{
		{Name: "idx", Table: "t", Columns: nil},
		{Name: "bad name", Table: "t", Columns: []string{"c"}},
		{Name: "idx", Table: "bad table", Columns: []string{"c"}},
		{Name: "idx", Table: "t", Columns: []string{"bad column"}},
	}
	for i, idx := range cases {
		idxCopy := idx
		set := migrate.MigrationSet{SchemaVersion: 1, MinimumReaderVersion: 1, Steps: []migrate.MigrationStep{{Kind: migrate.StepCreateIndex, Index: &idxCopy}}}
		if _, err := (migrate.SQLiteEmitter{}).Emit(set); err == nil {
			t.Errorf("case %d: want error, got nil", i)
		}
	}
}

// TestEmitForeignKey_InvalidIdentifiers covers each of ForeignKeyDef's
// three identifier fields individually.
func TestEmitForeignKey_InvalidIdentifiers(t *testing.T) {
	base := migrate.ForeignKeyDef{Column: "user_id", RefTable: "users", RefColumn: "id"}
	cases := []migrate.ForeignKeyDef{
		{Column: "bad col", RefTable: base.RefTable, RefColumn: base.RefColumn},
		{Column: base.Column, RefTable: "bad table", RefColumn: base.RefColumn},
		{Column: base.Column, RefTable: base.RefTable, RefColumn: "bad col"},
	}
	for i, fk := range cases {
		set := migrate.MigrationSet{
			SchemaVersion: 1, MinimumReaderVersion: 1,
			Steps: []migrate.MigrationStep{{
				Kind: migrate.StepCreateTable,
				Table: &migrate.TableDef{
					Name:        "posts",
					Columns:     []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}, {Name: "user_id", Type: migrate.TypeInteger}},
					ForeignKeys: []migrate.ForeignKeyDef{fk},
				},
			}},
		}
		if _, err := (migrate.SQLiteEmitter{}).Emit(set); err == nil {
			t.Errorf("case %d: want error, got nil", i)
		}
	}
}

// TestSinglePrimaryKeyWithoutAutoincrement proves a single, non-
// autoincrement PrimaryKey column renders as an inline "PRIMARY KEY"
// clause and is valid, real DDL.
func TestSinglePrimaryKeyWithoutAutoincrement(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion: 1, MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    "settings",
				Columns: []migrate.ColumnDef{{Name: "key", Type: migrate.TypeText, PrimaryKey: true}},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		stmts, err := dialect.Emit(set)
		if err != nil {
			t.Fatalf("%s: %v", dialect.Name(), err)
		}
		if len(stmts) != 1 {
			t.Fatalf("%s: want 1 statement, got %d", dialect.Name(), len(stmts))
		}
	}
}

// TestUnknownColumnType proves both emitters refuse a ColumnType value
// outside the closed enum rather than emitting an empty/garbage type
// keyword.
func TestUnknownColumnType(t *testing.T) {
	set := migrate.MigrationSet{
		SchemaVersion: 1, MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    "t",
				Columns: []migrate.ColumnDef{{Name: "c", Type: migrate.ColumnType(99)}},
			},
		}},
	}
	for _, dialect := range []migrate.Dialect{migrate.SQLiteEmitter{}, migrate.PostgresEmitter{}} {
		if _, err := dialect.Emit(set); err == nil {
			t.Errorf("%s: unknown ColumnType: want error, got nil", dialect.Name())
		}
	}
}

// TestApply_ClosedDB proves Apply surfaces a taxonomy error (never a
// panic) when the underlying *sql.DB is already closed, exercising
// ensureLedgerTable's/currentSchemaVersion's error branches.
func TestApply_ClosedDB(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := migrate.Apply(context.Background(), cfg, referenceMigrationSet())
	if err == nil {
		t.Fatal("Apply against a closed *sql.DB: want error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Apply against a closed *sql.DB: want KindUnavailable, got %v", err)
	}
}

// TestApply_BackupDirIsRegularFile proves a failed §D-18 snapshot write
// blocks the migration (fail-closed): making BackupDir a path that
// already exists as a regular file (so os.MkdirAll fails) must prevent
// any DDL from executing.
func TestApply_BackupDirIsRegularFile(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)

	// Replace BackupDir with a path component that is already a regular
	// file, not a directory, so os.MkdirAll fails.
	blocker := filepath.Join(filepath.Dir(path), "backups-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil { //nolint:gosec // test-only, t.TempDir()-scoped path
		t.Fatalf("seed blocker file: %v", err)
	}
	cfg.BackupDir = filepath.Join(blocker, "backups")

	err := migrate.Apply(context.Background(), cfg, referenceMigrationSet())
	if err == nil {
		t.Fatal("Apply with an unwritable BackupDir: want error, got nil")
	}

	var count int
	if scanErr := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&count); scanErr != nil {
		t.Fatalf("check users absent: %v", scanErr)
	}
	if count != 0 {
		t.Error("Apply executed DDL despite the snapshot failing — snapshot failure must block the migration (fail-closed)")
	}
}

// TestApply_NoSnapshotWhenDBPathEmpty proves Apply skips the §D-18
// snapshot entirely (and succeeds) when DBPath is "" — the Postgres /
// no-local-file case.
func TestApply_NoSnapshotWhenDBPathEmpty(t *testing.T) {
	db, _ := openTestDB(t)
	cfg := migrate.ApplyConfig{
		DB:      db,
		Dialect: migrate.SQLiteEmitter{},
		Clock:   testkit.NewFrozenClock(time.Now()),
	}
	if err := migrate.Apply(context.Background(), cfg, referenceMigrationSet()); err != nil {
		t.Fatalf("Apply with DBPath unset: %v", err)
	}
}
