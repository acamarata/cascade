// Purpose: TestGoldenSQLite/TestGoldenPostgres — byte-for-byte diff of each
//
//	emitter's output against internal/storage/migrate/testdata/golden/
//	{sqlite,postgres}, run against the SAME canonical referenceMigrationSet
//	on both dialects (this ticket's AC). See testdata/README.md for the
//	Postgres fixture's real-counterpart provenance.
//
// SPORT: internal.storage.migrate.SQLiteEmitter/ADDED,
//
//	internal.storage.migrate.PostgresEmitter/ADDED (P1-E02-W1-S02-T3).
package migrate_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// update, when set via `go test ./internal/storage/migrate/... -run Golden
// -update`, rewrites the golden fixtures from the emitters' current
// output instead of diffing against them. Never set in CI/normal runs —
// intentionally NOT wired to any check in the ticket's checks: list.
var update = flag.Bool("update", false, "update golden fixtures")

// referenceMigrationSet is the single canonical MigrationSet both golden
// tests emit from: a "users" table (autoincrement integer primary key,
// TEXT/REAL/BLOB columns, a NOT NULL + UNIQUE column), a "posts" table
// with a FOREIGN KEY back to users (ON DELETE CASCADE), and an index on
// posts(user_id). This exercises every construct both emitters support.
func referenceMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{
			{
				Kind:        migrate.StepCreateTable,
				Description: "create users table",
				Table: &migrate.TableDef{
					Name: "users",
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true},
						{Name: "email", Type: migrate.TypeText, NotNull: true, Unique: true},
						{Name: "balance", Type: migrate.TypeReal, NotNull: true},
						{Name: "avatar", Type: migrate.TypeBlob},
					},
				},
			},
			{
				Kind:        migrate.StepCreateTable,
				Description: "create posts table with FK to users",
				Table: &migrate.TableDef{
					Name: "posts",
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true},
						{Name: "user_id", Type: migrate.TypeInteger, NotNull: true},
						{Name: "body", Type: migrate.TypeText, NotNull: true},
					},
					ForeignKeys: []migrate.ForeignKeyDef{
						{Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "CASCADE"},
					},
				},
			},
			{
				Kind:        migrate.StepCreateIndex,
				Description: "index posts by user_id",
				Index: &migrate.IndexDef{
					Name:    "idx_posts_user_id",
					Table:   "posts",
					Columns: []string{"user_id"},
				},
			},
		},
	}
}

// TestGoldenSQLite diffs SQLiteEmitter's output for referenceMigrationSet
// against testdata/golden/sqlite/0001_reference.sql.
func TestGoldenSQLite(t *testing.T) {
	checkGolden(t, migrate.SQLiteEmitter{}, filepath.Join("testdata", "golden", "sqlite", "0001_reference.sql"))
}

// TestGoldenPostgres diffs PostgresEmitter's output for
// referenceMigrationSet against testdata/golden/postgres/0001_reference.sql
// — see testdata/README.md for how this fixture was captured against a
// real Postgres instance.
func TestGoldenPostgres(t *testing.T) {
	checkGolden(t, migrate.PostgresEmitter{}, filepath.Join("testdata", "golden", "postgres", "0001_reference.sql"))
}

func checkGolden(t *testing.T, dialect migrate.Dialect, goldenPath string) {
	t.Helper()
	stmts, err := dialect.Emit(referenceMigrationSet())
	if err != nil {
		t.Fatalf("%s: Emit: %v", dialect.Name(), err)
	}
	got := strings.Join(stmts, "\n\n") + "\n"

	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil { //nolint:gosec // test-only golden fixture regeneration, fixed repo-relative path
			t.Fatalf("update golden %s: %v", goldenPath, err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath) //nolint:gosec // test-only, fixed repo-relative path under testdata
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", goldenPath, err)
	}
	if got != string(want) {
		t.Errorf("%s: emitted DDL does not match %s (byte-for-byte)\n--- got ---\n%s\n--- want ---\n%s", dialect.Name(), goldenPath, got, string(want))
	}
}
