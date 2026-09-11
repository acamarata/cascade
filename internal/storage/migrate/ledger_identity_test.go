package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestApply_DifferentSetIDsMaySharedSchemaVersion reproduces
// R-16.77's described defect against today's (pre-fix) HEAD: two sets
// that would carry different SetID values under the fix, sharing a
// SchemaVersion, applied against ONE *sql.DB. Under the old global
// ledger, raising the file's global schema_version (via an unrelated
// third set) makes the second same-numbered set's own Apply call hit the
// silent no-op branch (set.SchemaVersion < onDisk) — its table is never
// created — rather than erroring.
func TestApply_DifferentSetIDsMaySharedSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "redcheck.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock}

	setA := oneTableSet("a", 1, 1, "set_a_table")
	if err := migrate.Apply(context.Background(), cfg, setA); err != nil {
		t.Fatalf("apply setA: %v", err)
	}

	// setC is an unrelated set that raises the file's GLOBAL
	// schema_version to 2 -- exactly the "conversation" vs "repo" both
	// at 7 while scope/lifecycle/registry/jobs/usage already pushed the
	// file's max version higher" shape R-16.77 describes.
	setC := oneTableSet("c", 2, 2, "set_c_table")
	if err := migrate.Apply(context.Background(), cfg, setC); err != nil {
		t.Fatalf("apply setC: %v", err)
	}

	// setB shares setA's SchemaVersion (1) but is a DIFFERENT set (a
	// different table). Under per-set identity this must still create
	// set_b_table. Under today's global-ledger HEAD it does not: onDisk
	// is now 2 (setC's global bump), so setB.SchemaVersion(1) < onDisk(2)
	// takes the silent no-op branch and set_b_table is never created.
	// ReaderCeiling is deliberately generous (not self-referential),
	// exactly like a caller who computes its ceiling as a max() over
	// every other domain's SchemaVersion to avoid the downgrade refusal
	// (cmd/cascade/daemon_unix_store.go's pre-fix runtimeReaderCeiling is
	// the real-world shape). That avoids *SchemaDowngradeError but does
	// not save it from the OTHER branch: onDisk(2) > SchemaVersion(1)
	// still takes the silent no-op path.
	setB := oneTableSet("b", 1, 99, "set_b_table")
	if err := migrate.Apply(context.Background(), cfg, setB); err != nil {
		t.Fatalf("apply setB: %v", err)
	}

	for _, table := range []string{"set_a_table", "set_b_table", "set_c_table"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s missing after Apply (count=%d) -- setB's tables were silently never created", table, count)
		}
	}
}

// oneTableSet builds a minimal single-table MigrationSet, split out of
// TestApply_DifferentSetIDsMaySharedSchemaVersion to keep that test under
// Art.10.3's 50-line function cap.
func oneTableSet(setID string, schemaVersion, readerCeiling int, tableName string) migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         setID,
		SchemaVersion: schemaVersion,
		ReaderCeiling: readerCeiling,
		Steps: []migrate.MigrationStep{{
			Kind:  migrate.StepCreateTable,
			Table: &migrate.TableDef{Name: tableName, Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}}},
		}},
	}
}
