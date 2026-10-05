package learn

// Purpose: the learn-config migration set: idempotent over a db that already
//   holds the "learn" set, leaving that set's ledger rows and tables alone.
// SPORT: learn/migration_config_test (P1-LRN-01).

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// applyConfigSchema runs ConfigMigrationSet against db.
func applyConfigSchema(db *sql.DB) error {
	return migrate.Apply(context.Background(), migrate.ApplyConfig{
		DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock(),
	}, ConfigMigrationSet())
}

// ledgerRows returns the ledger rows of one set, in id order.
func ledgerRows(t *testing.T, db *sql.DB, setID string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id || ':' || schema_version || ':' || checksum || ':' || applied_at
		FROM applied_migrations WHERE set_id = ? ORDER BY id`, setID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ledger rows: %v", err)
	}
	return out
}

// configSetTables checks the set's identity and returns its jobs_ tables.
func configSetTables(t *testing.T, set migrate.MigrationSet) []string {
	t.Helper()
	if set.SetID != "learn-config" || set.SchemaVersion != 1 || set.ReaderCeiling != 1 || len(set.Steps) != 3 {
		t.Fatalf("ConfigMigrationSet = %s v%d ceiling %d with %d steps", set.SetID, set.SchemaVersion, set.ReaderCeiling, len(set.Steps))
	}
	if set.SetID == MigrationSet().SetID {
		t.Fatal("learn-config shares the learn SetID")
	}
	var tables []string
	for _, s := range set.Steps {
		if s.Table == nil || !strings.HasPrefix(s.Table.Name, "jobs_") {
			t.Fatalf("step %q is not a jobs_ table", s.Description)
		}
		tables = append(tables, s.Table.Name)
	}
	return tables
}

func TestLearnedConfigMigrationIdempotent(t *testing.T) {
	isolateHome(t)
	set := ConfigMigrationSet()
	tables := configSetTables(t, set)
	db := openMigratedDB(t, tmplOutcomeJobs)
	learnBefore := ledgerRows(t, db, "learn")
	schemaBefore := schemaSQL(t, db)
	if len(learnBefore) == 0 {
		t.Fatal("template db holds no learn ledger rows; the test would be vacuous")
	}
	var first []string
	for i := 0; i < 2; i++ {
		if err := applyConfigSchema(db); err != nil {
			t.Fatalf("apply learn-config #%d: %v", i+1, err)
		}
		got := ledgerRows(t, db, "learn-config")
		if i == 0 {
			first = got
		}
		if len(got) != len(set.Steps) || !slices.Equal(got, first) {
			t.Fatalf("learn-config ledger after apply #%d = %v, want %d rows unchanged by a re-apply", i+1, got, len(set.Steps))
		}
		for _, row := range got {
			if !strings.Contains(row, ":1:") {
				t.Fatalf("learn-config ledger row %s is not at schema_version 1", row)
			}
		}
	}
	if got := ledgerRows(t, db, "learn"); !slices.Equal(got, learnBefore) {
		t.Fatalf("learn ledger rows changed: %v -> %v", learnBefore, got)
	}
	after := schemaSQL(t, db)
	for _, obj := range schemaBefore {
		if !slices.Contains(after, obj) {
			t.Fatalf("existing schema object changed or vanished: %s", obj)
		}
	}
	for _, name := range tables {
		if n := countQuery(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='`+name+`'`); n != 1 {
			t.Fatalf("table %s missing after apply", name)
		}
	}
	for _, obj := range after {
		if !slices.Contains(schemaBefore, obj) && !strings.Contains(obj, "jobs_learned_config") {
			t.Fatalf("learn-config created an object outside its tables: %s", obj)
		}
	}
}
