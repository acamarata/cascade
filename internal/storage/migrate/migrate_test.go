// Purpose: Apply-level integration tests — TestIdempotentApply,
//
//	TestDowngradeRefusal, TestMigrationConflict, TestPreMigrationSnapshotExists
//	— all run against a REAL modernc-sqlite database under t.TempDir()
//	(Art.2/Art.7.1), never a mock.
//
// SPORT: internal.storage.migrate.Ledger/ADDED, internal.storage.migrate.
//
//	Snapshot/ADDED (P1-E02-W1-S02-T3).
package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// openTestDB opens a fresh real modernc-sqlite database under t.TempDir()
// and returns it plus its file path, with Close registered via
// t.Cleanup.
func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migrate-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func applyConfig(t *testing.T, db *sql.DB, dbPath string) migrate.ApplyConfig {
	t.Helper()
	return migrate.ApplyConfig{
		DB:        db,
		Dialect:   migrate.SQLiteEmitter{},
		Clock:     testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)),
		DBPath:    dbPath,
		BackupDir: filepath.Join(filepath.Dir(dbPath), "backups"),
	}
}

func ledgerRows(t *testing.T, db *sql.DB) []struct {
	Version  int
	Checksum string
} {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT schema_version, checksum FROM "applied_migrations" ORDER BY id`)
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []struct {
		Version  int
		Checksum string
	}
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			t.Fatalf("scan ledger row: %v", err)
		}
		out = append(out, struct {
			Version  int
			Checksum string
		}{v, c})
	}
	return out
}

// TestIdempotentApply applies referenceMigrationSet twice against a real
// database and asserts identical ledger row count and checksums, exit 0,
// no duplicate DDL execution the second time (proven indirectly: a
// duplicate CREATE TABLE would still succeed under IF NOT EXISTS, so the
// real proof is the ledger row count staying at 3, not doubling to 6).
func TestIdempotentApply(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)
	set := referenceMigrationSet()

	if err := migrate.Apply(context.Background(), cfg, set); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	first := ledgerRows(t, db)
	if len(first) != len(set.Steps) {
		t.Fatalf("after first Apply: got %d ledger rows, want %d", len(first), len(set.Steps))
	}

	if err := migrate.Apply(context.Background(), cfg, set); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	second := ledgerRows(t, db)
	if len(second) != len(first) {
		t.Fatalf("after second Apply: got %d ledger rows, want %d (unchanged)", len(second), len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("ledger row %d changed across re-apply: %+v -> %+v", i, first[i], second[i])
		}
	}

	// Both tables and the index are real and queryable.
	if _, err := db.Exec(`INSERT INTO users (email, balance) VALUES ('x@y.com', 1)`); err != nil {
		t.Fatalf("insert into users after Apply: %v", err)
	}
}

// TestDowngradeRefusal seeds the ledger at schema_version=5 (via a real
// Apply call), then applies a set whose MinimumReaderVersion=3 and asserts
// *migrate.SchemaDowngradeError with the correct on-disk and binary
// version fields populated — the opener must never silently open a schema
// newer than it understands.
func TestDowngradeRefusal(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)

	seed := migrate.MigrationSet{
		SchemaVersion:        5,
		MinimumReaderVersion: 5,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    "seed_table",
				Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true}},
			},
		}},
	}
	if err := migrate.Apply(context.Background(), cfg, seed); err != nil {
		t.Fatalf("seed Apply: %v", err)
	}

	tooOld := migrate.MigrationSet{
		SchemaVersion:        6,
		MinimumReaderVersion: 3,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    "never_created",
				Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}},
			},
		}},
	}
	err := migrate.Apply(context.Background(), cfg, tooOld)
	assertDowngradeRefused(t, db, err)
}

// assertDowngradeRefused checks the *SchemaDowngradeError shape and
// confirms the refused migration's DDL never ran, split out of
// TestDowngradeRefusal to keep the test function under Art.10.3's
// 50-line cap.
func assertDowngradeRefused(t *testing.T, db *sql.DB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Apply with stale MinimumReaderVersion: want *SchemaDowngradeError, got nil")
	}
	var downgrade *migrate.SchemaDowngradeError
	if !errors.As(err, &downgrade) {
		t.Fatalf("Apply with stale MinimumReaderVersion: want *SchemaDowngradeError, got %T: %v", err, err)
	}
	if downgrade.OnDiskVersion != 5 {
		t.Errorf("OnDiskVersion = %d, want 5", downgrade.OnDiskVersion)
	}
	if downgrade.MinimumReaderVersion != 3 {
		t.Errorf("MinimumReaderVersion = %d, want 3", downgrade.MinimumReaderVersion)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='never_created'`).Scan(&count); err != nil {
		t.Fatalf("check never_created absent: %v", err)
	}
	if count != 0 {
		t.Error("Apply executed DDL despite downgrade refusal — the opener must fail BEFORE any DDL runs")
	}
}

// TestMigrationConflict applies a set at schema_version=1, then re-applies
// the same schema_version with step 1's content changed, and asserts
// *migrate.MigrationConflictError identifying the diverged step.
func TestMigrationConflict(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)

	original := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{
			{Kind: migrate.StepCreateTable, Table: &migrate.TableDef{
				Name: "a", Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}},
			}},
			{Kind: migrate.StepCreateTable, Table: &migrate.TableDef{
				Name: "b", Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}},
			}},
		},
	}
	if err := migrate.Apply(context.Background(), cfg, original); err != nil {
		t.Fatalf("original Apply: %v", err)
	}

	changed := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{
			original.Steps[0], // unchanged
			{Kind: migrate.StepCreateTable, Table: &migrate.TableDef{
				Name: "b_renamed", Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}},
			}}, // content changed at the same position
		},
	}
	err := migrate.Apply(context.Background(), cfg, changed)
	if err == nil {
		t.Fatal("Apply with a changed step at an already-applied position: want *MigrationConflictError, got nil")
	}
	var conflict *migrate.MigrationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want *MigrationConflictError, got %T: %v", err, err)
	}
	if conflict.StepIndex != 1 {
		t.Errorf("StepIndex = %d, want 1", conflict.StepIndex)
	}
	if conflict.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", conflict.SchemaVersion)
	}
}

// TestPreMigrationSnapshotExists applies a migration against a fresh
// database and asserts the §D-18 pre-migrate-v0.db snapshot exists and is
// non-empty in BackupDir before any caller-visible success.
func TestPreMigrationSnapshotExists(t *testing.T) {
	db, path := openTestDB(t)
	cfg := applyConfig(t, db, path)

	if err := migrate.Apply(context.Background(), cfg, referenceMigrationSet()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	snapshotPath := filepath.Join(cfg.BackupDir, "pre-migrate-v0.db")
	info, err := os.Stat(snapshotPath)
	if err != nil {
		t.Fatalf("stat snapshot %s: %v", snapshotPath, err)
	}
	if info.Size() == 0 {
		t.Errorf("snapshot %s exists but is empty", snapshotPath)
	}
}

// TestApplyConfig_Validation exercises ApplyConfig's required-field
// checks directly (no database needed — they fail before any I/O).
func TestApplyConfig_Validation(t *testing.T) {
	valid := migrate.ApplyConfig{DB: &sql.DB{}, Dialect: migrate.SQLiteEmitter{}, Clock: testkit.NewFrozenClock(time.Now())}
	set := migrate.MigrationSet{SchemaVersion: 1, MinimumReaderVersion: 1}

	cases := []struct {
		name string
		cfg  migrate.ApplyConfig
		set  migrate.MigrationSet
	}{
		{"missing DB", migrate.ApplyConfig{Dialect: valid.Dialect, Clock: valid.Clock}, set},
		{"missing Dialect", migrate.ApplyConfig{DB: valid.DB, Clock: valid.Clock}, set},
		{"missing Clock", migrate.ApplyConfig{DB: valid.DB, Dialect: valid.Dialect}, set},
		{"DBPath without BackupDir", migrate.ApplyConfig{DB: valid.DB, Dialect: valid.Dialect, Clock: valid.Clock, DBPath: "/tmp/x.db"}, set},
		{"SchemaVersion zero", valid, migrate.MigrationSet{SchemaVersion: 0, MinimumReaderVersion: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := migrate.Apply(context.Background(), tc.cfg, tc.set); err == nil {
				t.Errorf("want validation error, got nil")
			}
		})
	}
}
