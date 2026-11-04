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
	if set.SetID != "learn-config" || set.SchemaVersion != 2 || set.ReaderCeiling != 2 || len(set.Steps) != 6 {
		t.Fatalf("ConfigMigrationSet = %s v%d ceiling %d with %d steps", set.SetID, set.SchemaVersion, set.ReaderCeiling, len(set.Steps))
	}
	if set.SetID == MigrationSet().SetID {
		t.Fatal("learn-config shares the learn SetID")
	}
	var tables []string
	for _, s := range set.Steps {
		if s.Kind == migrate.StepCreateIndex && s.Index != nil && strings.HasPrefix(s.Index.Table, "jobs_") {
			continue
		}
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
			if !strings.Contains(row, ":2:") {
				t.Fatalf("learn-config ledger row %s is not at schema_version 2", row)
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
		if !slices.Contains(schemaBefore, obj) && !ownedByConfigSet(obj) {
			t.Fatalf("learn-config created an object outside its tables: %s", obj)
		}
	}
}

// ownedByConfigSet reports whether a schema object belongs to learn-config.
func ownedByConfigSet(obj string) bool {
	for _, name := range []string{"jobs_learned_config", tableProposalTrigger, tableFindingFeed} {
		if strings.Contains(obj, name) {
			return true
		}
	}
	return false
}

// columnNames returns table's columns in declaration order.
func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	return out
}

func TestProposalMigrationIdempotent(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	db := openTestDB(t)
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, cfg, configMigrationSetAt(1)); err != nil {
		t.Fatalf("apply learn-config v1: %v", err)
	}
	v1 := ledgerRows(t, db, "learn-config")
	if len(v1) != 3 || countQuery(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'jobs_learn_proposal_trigger'`) != 0 {
		t.Fatalf("v1 database: ledger %v, or the trigger table already exists", v1)
	}
	if _, err := db.Exec(`INSERT INTO jobs_learned_config (id, target_id, scope_ref, label, confidence, tier, areas,
		loosens_bound, source_kind, source_ref, created_at, last_verified_at, success_count, failure_count, version)
		VALUES ('v1-row', 'ci.local_timeout', '', '', 0.5, 'safe', '', 0, 'detector', 'd', 1, 1, 0, 0, 0)`); err != nil {
		t.Fatalf("seed v1 row: %v", err)
	}
	var first []string
	for i := 0; i < 3; i++ {
		if err := applyConfigSchema(db); err != nil {
			t.Fatalf("apply learn-config v2 #%d: %v", i+1, err)
		}
		got := ledgerRows(t, db, "learn-config")
		if i == 0 {
			first = got
		}
		if len(got) != 9 || !slices.Equal(got, first) || !slices.Equal(got[:3], v1) {
			t.Fatalf("ledger after v2 apply #%d = %v, want the 3 v1 rows plus 6 v2 rows, unchanged by a re-apply", i+1, got)
		}
	}
	if err := migrate.Apply(ctx, cfg, configMigrationSetAt(1)); err == nil {
		t.Fatal("a v1 reader opened a v2 schema; the ceiling must refuse it")
	}
	if n := countQuery(t, db, "SELECT COUNT(*) FROM jobs_learned_config WHERE id = 'v1-row'"); n != 1 {
		t.Fatalf("v1 row lost on upgrade: %d", n)
	}
	assertProposalTables(t, db)
}

// assertProposalTables checks the v2 trigger and finding-feed tables'
// columns, the trigger signature index and the trigger foreign key.
func assertProposalTables(t *testing.T, db *sql.DB) {
	t.Helper()
	wantTrigger := []string{"config_id", "trigger_signature", "occurrence_count", "distinct_source_count",
		"beneficiary_lane_tier", "beneficiary_node_id", "window_start", "window_end", "change_kind"}
	if got := columnNames(t, db, "jobs_learn_proposal_trigger"); !slices.Equal(got, wantTrigger) {
		t.Fatalf("trigger columns = %v, want %v", got, wantTrigger)
	}
	if got := columnNames(t, db, "jobs_learn_finding_feed"); !slices.Equal(got, []string{"outcome_id", "fed_at"}) {
		t.Fatalf("finding feed columns = %v", got)
	}
	var idxCols []string
	rows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, indexTriggerSignature)
	if err != nil {
		t.Fatalf("index_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan index column: %v", err)
		}
		idxCols = append(idxCols, n)
	}
	if !slices.Equal(idxCols, []string{"trigger_signature", "window_end"}) {
		t.Fatalf("trigger index columns = %v", idxCols)
	}
	var fk string
	if err := db.QueryRow(`SELECT "table" || '.' || "to" FROM pragma_foreign_key_list('jobs_learn_proposal_trigger')`).Scan(&fk); err != nil || fk != "jobs_learned_config.id" {
		t.Fatalf("trigger foreign key = (%q, %v), want jobs_learned_config.id", fk, err)
	}
}
