package learn

// Purpose: the learn-routing migration set: idempotent over a db that already
// holds the "learn" and "learn-config" sets, leaving those sets' ledger
// rows and tables alone.
// SPORT: learn/migration_routing_test (P1-LRN-07a).

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// applyRoutingSchema runs RoutingMigrationSet against db.
func applyRoutingSchema(db *sql.DB) error {
	return migrate.Apply(context.Background(), migrate.ApplyConfig{
		DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock(),
	}, RoutingMigrationSet())
}

// routingLedgerRows returns the ledger rows of one set, in id order.
func routingLedgerRows(t *testing.T, db *sql.DB, setID string) []string {
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

// routingSetTables checks the set's identity and returns its jobs_ tables.
func routingSetTables(t *testing.T, set migrate.MigrationSet) []string {
	t.Helper()
	if set.SetID != "learn-routing" || set.SchemaVersion != 1 || set.ReaderCeiling != 1 || len(set.Steps) != 4 {
		t.Fatalf("RoutingMigrationSet = %s v%d ceiling %d with %d steps", set.SetID, set.SchemaVersion, set.ReaderCeiling, len(set.Steps))
	}
	if set.SetID == MigrationSet().SetID {
		t.Fatal("learn-routing shares the learn SetID")
	}
	if set.SetID == ConfigMigrationSet().SetID {
		t.Fatal("learn-routing shares the learn-config SetID")
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

func TestRoutingMigrationIdempotent(t *testing.T) {
	isolateHome(t)
	set := RoutingMigrationSet()
	tables := routingSetTables(t, set)
	db := openMigratedDB(t, tmplOutcomeJobs)

	if err := applyConfigSchema(db); err != nil {
		t.Fatalf("apply learn-config baseline: %v", err)
	}

	learnBefore := routingLedgerRows(t, db, "learn")
	configBefore := routingLedgerRows(t, db, "learn-config")
	schemaBefore := schemaSQL(t, db)
	if len(learnBefore) == 0 {
		t.Fatal("template db holds no learn ledger rows; the test would be vacuous")
	}

	checkRoutingReapply(t, db, len(set.Steps))

	if got := routingLedgerRows(t, db, "learn"); !slices.Equal(got, learnBefore) {
		t.Fatalf("learn ledger rows changed: %v -> %v", learnBefore, got)
	}
	if got := routingLedgerRows(t, db, "learn-config"); !slices.Equal(got, configBefore) {
		t.Fatalf("learn-config ledger rows changed: %v -> %v", configBefore, got)
	}

	after := schemaSQL(t, db)
	for _, obj := range schemaBefore {
		if !slices.Contains(after, obj) {
			t.Fatalf("existing schema object changed or vanished: %s", obj)
		}
	}
	for _, name := range tables {
		checkRoutingTable(t, db, name)
	}
	checkRoutingObjectsOwned(t, schemaBefore, after)
}

// checkRoutingReapply applies the learn-routing set twice and requires the
// same steps-many ledger rows, all at schema_version 1, after each apply.
func checkRoutingReapply(t *testing.T, db *sql.DB, steps int) {
	t.Helper()
	var first []string
	for i := 0; i < 2; i++ {
		if err := applyRoutingSchema(db); err != nil {
			t.Fatalf("apply learn-routing #%d: %v", i+1, err)
		}
		got := routingLedgerRows(t, db, "learn-routing")
		if i == 0 {
			first = got
		}
		if len(got) != steps || !slices.Equal(got, first) {
			t.Fatalf("learn-routing ledger after apply #%d = %v, want %d rows unchanged by a re-apply", i+1, got, steps)
		}
		for _, row := range got {
			if !strings.Contains(row, ":1:") {
				t.Fatalf("learn-routing ledger row %s is not at schema_version 1", row)
			}
		}
	}
}

// checkRoutingTable requires table name to exist exactly once and to hold
// no free-text or model_id column.
func checkRoutingTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if n := countQuery(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='`+name+`'`); n != 1 {
		t.Fatalf("table %s missing after apply", name)
	}
	rows, err := db.Query(`PRAGMA table_info('` + name + `')`)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", name, err)
	}
	hasFreeText := false
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma table_info(%s): %v", name, err)
		}
		if strings.EqualFold(colName, "free_text") || strings.EqualFold(colName, "model_id") {
			hasFreeText = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma table_info(%s): %v", name, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pragma table_info(%s): %v", name, err)
	}
	if hasFreeText {
		t.Fatalf("table %s has a free-text column", name)
	}
}

// checkRoutingObjectsOwned requires every new schema object to belong to
// one of the learn-routing tables.
func checkRoutingObjectsOwned(t *testing.T, before, after []string) {
	t.Helper()
	for _, obj := range after {
		if !slices.Contains(before, obj) && !strings.Contains(obj, "jobs_lane_posterior") &&
			!strings.Contains(obj, "jobs_family_pair") &&
			!strings.Contains(obj, "jobs_folded_marker_inbound") &&
			!strings.Contains(obj, "jobs_folded_marker_outbound") {
			t.Fatalf("learn-routing created an object outside its tables: %s", obj)
		}
	}
}
