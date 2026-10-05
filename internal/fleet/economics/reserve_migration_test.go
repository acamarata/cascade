package economics

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func TestReservationSchemaVersionIsOne(t *testing.T) {
	set := ReservationMigrationSet()
	if ReservationSchemaVersion != 1 || set.SchemaVersion != 1 || set.ReaderCeiling != 1 || set.SetID != "fleet-economics-reservation" {
		t.Errorf("set = %s v%d ceiling %d, want fleet-economics-reservation v1 ceiling 1", set.SetID, set.SchemaVersion, set.ReaderCeiling)
	}
}

// TestReservationMigrationIdempotent: the amended v1 set creates the
// table with the placement columns, repo_id, scope_globs and the unique
// execution_id index, and applies twice without error.
func TestReservationMigrationIdempotent(t *testing.T) {
	db := openReservationTestDB(t)
	ctx := t.Context()
	if err := ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second ApplyReservationSchema: %v", err)
	}
	cols := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, tableReservation)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	for _, c := range []string{"execution_id", "node_id", "selected_tier", "sensitivity", "decision_id", "repo_id", "scope_globs"} {
		if !cols[c] {
			t.Errorf("column %s missing from %s", c, tableReservation)
		}
	}
	for idx, unique := range map[string]int{
		"idx_jobs_reservation_domain_state": 0, "idx_jobs_reservation_scope_state": 0,
		"idx_jobs_reservation_project_state": 0, "idx_jobs_reservation_sweep": 0,
		"idx_jobs_reservation_execution": 1,
	} {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT "unique" FROM pragma_index_list(?) WHERE name = ?`, tableReservation, idx).Scan(&got); err != nil || got != unique {
			t.Errorf("index %s unique=%d err=%v, want unique=%d", idx, got, err, unique)
		}
	}
}

// oldReservationV1 is the jobs_reservation v1 definition as it stood
// before this ledger amended it in place (22 columns, four indexes).
func oldReservationV1() migrate.MigrationSet {
	names := []string{"id", "job_id", "project_id", "lane_id", "domain_id", "scope_id", "kind", "estimate_json",
		"actual_json", "actual_source", "base_price", "price_table_version", "scarce_units", "permit_id",
		"worktree_id", "lease_ids_json", "steps_json", "state", "owner_epoch", "heartbeat_at", "expires_at", "created"}
	types := map[string]migrate.ColumnType{"base_price": migrate.TypeReal, "scarce_units": migrate.TypeReal,
		"heartbeat_at": migrate.TypeInteger, "expires_at": migrate.TypeInteger, "created": migrate.TypeInteger}
	cols := make([]migrate.ColumnDef, 0, len(names))
	for i, n := range names {
		typ, ok := types[n]
		if !ok {
			typ = migrate.TypeText
		}
		cols = append(cols, migrate.ColumnDef{Name: n, Type: typ, PrimaryKey: i == 0, NotNull: true})
	}
	return migrate.MigrationSet{
		SetID: "fleet-economics-reservation", SchemaVersion: 1, ReaderCeiling: 1,
		Steps: []migrate.MigrationStep{{Kind: migrate.StepCreateTable, Table: &migrate.TableDef{Name: tableReservation, Columns: cols},
			Description: "jobs_reservation: the atomic-reservation ledger row, before the amendment"}},
	}
}

// TestReservationV1AmendConflictsOnOldLedger: a database whose migrate
// ledger holds the old v1 step checksums makes Apply return the migrate
// checksum-conflict error rather than silently skipping the amendment.
func TestReservationV1AmendConflictsOnOldLedger(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old-v1.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, cfg, oldReservationV1()); err != nil {
		t.Fatalf("apply old v1: %v", err)
	}
	err = ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", "")
	var conflict *migrate.MigrationConflictError
	if !errors.As(err, &conflict) || conflict.SchemaVersion != 1 || conflict.StepIndex != 0 {
		t.Fatalf("ApplyReservationSchema over the old v1 ledger = %v, want MigrationConflictError at v1 step 0", err)
	}
}

func TestApplyReservationSchemaRefusesNilArgs(t *testing.T) {
	ctx := t.Context()
	if err := ApplyReservationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); !isKindInvalidInput(err) {
		t.Errorf("ApplyReservationSchema(nil db) = %v, want KindInvalidInput", err)
	}
	db := openReservationTestDB(t)
	if err := ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); !isKindInvalidInput(err) {
		t.Errorf("ApplyReservationSchema(nil clock) = %v, want KindInvalidInput", err)
	}
}
