package economics

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func TestReservationSchemaVersionIsOne(t *testing.T) {
	if ReservationSchemaVersion != 1 {
		t.Errorf("ReservationSchemaVersion = %d, want 1", ReservationSchemaVersion)
	}
	set := ReservationMigrationSet()
	if set.SchemaVersion != ReservationSchemaVersion || set.ReaderCeiling != ReservationSchemaVersion {
		t.Errorf("MigrationSet SchemaVersion/ReaderCeiling = %d/%d, want both %d", set.SchemaVersion, set.ReaderCeiling, ReservationSchemaVersion)
	}
}

func TestReservationMigrationCreatesTable(t *testing.T) {
	db := openReservationTestDB(t)
	row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, tableReservation)
	var name string
	if err := row.Scan(&name); err != nil {
		t.Fatalf("jobs_reservation table not found: %v", err)
	}
	if name != tableReservation {
		t.Errorf("table name = %q, want %q", name, tableReservation)
	}
}

func TestReservationMigrationIndexesPresent(t *testing.T) {
	db := openReservationTestDB(t)
	want := []string{
		"idx_jobs_reservation_domain_state",
		"idx_jobs_reservation_scope_state",
		"idx_jobs_reservation_project_state",
		"idx_jobs_reservation_sweep",
	}
	for _, idx := range want {
		row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx)
		var name string
		if err := row.Scan(&name); err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

func TestReservationMigrationApplyTwiceIsNoOp(t *testing.T) {
	db := openReservationTestDB(t)
	ctx := t.Context()
	// Applying again over the same already-migrated db must not error.
	if err := ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second ApplyReservationSchema: %v", err)
	}
}

func TestApplyReservationSchemaRefusesNilArgs(t *testing.T) {
	ctx := t.Context()
	if err := ApplyReservationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Error("ApplyReservationSchema(nil db) = nil error, want typed error")
	}
	db := openReservationTestDB(t)
	if err := ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("ApplyReservationSchema(nil clock) = nil error, want typed error")
	}
}
