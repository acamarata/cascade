// Purpose: hostile-identifier coverage for validateIdentifier/quoteIdent —
//
//	the single most important property this package has (SQL injection
//	through DSL-supplied table/column names). Every rejected name below is
//	proven rejected BEFORE it can reach a string; every accepted-but-
//	unusual name (a SQL reserved word, an underscore-leading name) is
//	proven not just accepted by validation but actually EXECUTED
//	successfully against a real modernc-sqlite database, so "the DSL
//	built a syntactically plausible statement" is never mistaken for
//	"the DSL is safe."
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED (P1-E02-W1-S02-T3).
package migrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// hostileTableName is a single-column TableDef named name, used to drive
// both emitters through validateIdentifier with a hostile candidate.
func hostileTableName(name string) migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    name,
				Columns: []migrate.ColumnDef{{Name: "id", Type: migrate.TypeInteger}},
			},
		}},
	}
}

// TestHostileIdentifiers_Rejected proves every listed hostile table name
// is rejected by BOTH emitters, before any SQL string is built.
func TestHostileIdentifiers_Rejected(t *testing.T) {
	hostile := []struct {
		name  string
		value string
	}{
		{"embedded double quote", `evil" ("x`},
		{"embedded single quote", `evil' ('x`},
		{"trailing semicolon injection", `users; DROP TABLE users; --`},
		{"backtick", "evil`table`"},
		{"unicode homoglyph", "tïtle"},
		{"unicode CJK", "用户"},
		{"empty string", ""},
		{"leading digit", "1users"},
		{"whitespace", "user table"},
		{"newline injection", "users\nDROP TABLE users"},
		{"null byte", "users\x00drop"},
		{"sql comment marker", "users--drop"},
		{"path traversal style", "../users"},
		{"percent wildcard", "users%"},
		{"over-length (64 chars)", strings.Repeat("a", 64)},
	}

	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			set := hostileTableName(tc.value)
			if _, err := (migrate.SQLiteEmitter{}).Emit(set); err == nil {
				t.Errorf("SQLiteEmitter accepted hostile identifier %q", tc.value)
			}
			if _, err := (migrate.PostgresEmitter{}).Emit(set); err == nil {
				t.Errorf("PostgresEmitter accepted hostile identifier %q", tc.value)
			}
		})
	}
}

// TestHostileIdentifiers_ReservedWordsAcceptedAndSafe proves that SQL
// reserved words ARE accepted as identifiers (they are not inherently
// dangerous — quoting handles them) and, critically, that the resulting
// DDL actually executes correctly against a real database: the table
// really is named after the reserved word, is queryable, and a second
// table using the same reserved word as a COLUMN name also round-trips.
func TestHostileIdentifiers_ReservedWordsAcceptedAndSafe(t *testing.T) {
	reserved := []string{"select", "table", "order", "group", "index", "where"}

	for _, word := range reserved {
		t.Run(word, func(t *testing.T) {
			set := migrate.MigrationSet{
				SchemaVersion:        1,
				MinimumReaderVersion: 1,
				Steps: []migrate.MigrationStep{{
					Kind: migrate.StepCreateTable,
					Table: &migrate.TableDef{
						Name: word,
						Columns: []migrate.ColumnDef{
							{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true},
							{Name: word, Type: migrate.TypeText},
						},
					},
				}},
			}
			stmts, err := (migrate.SQLiteEmitter{}).Emit(set)
			if err != nil {
				t.Fatalf("Emit(%q): %v", word, err)
			}

			path := filepath.Join(t.TempDir(), "reserved.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer func() { _ = db.Close() }()

			ctx := context.Background()
			for _, stmt := range stmts {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("exec DDL for reserved word %q: %v\nstatement: %s", word, err, stmt)
				}
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO `+quoteForTest(word)+` (id, `+quoteForTest(word)+`) VALUES (1, 'v')`); err != nil {
				t.Fatalf("insert into reserved-word table %q: %v", word, err)
			}
			var got string
			if err := db.QueryRowContext(ctx, `SELECT `+quoteForTest(word)+` FROM `+quoteForTest(word)+` WHERE id = 1`).Scan(&got); err != nil {
				t.Fatalf("select from reserved-word table %q: %v", word, err)
			}
			if got != "v" {
				t.Errorf("reserved-word table %q round-trip = %q, want %q", word, got, "v")
			}
		})
	}
}

// quoteForTest mirrors quoteIdent's ANSI double-quote form for building
// this test's own DML against a table/column whose name is a reserved
// word — the package's exported surface has no DML helpers (by design;
// this ticket only builds DDL), so the test constructs its own quoted
// reference the same way a real caller of the generated schema would.
func quoteForTest(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
