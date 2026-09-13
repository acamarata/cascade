// Purpose: the R-21.22 `reservation` table (`jobs_reservation`, the
//
//	`jobs` storage domain's table-prefix convention -- R-14.5/R-16.51's
//	closed domain list gains no new member) through the B/S-02.T3 typed
//	DSL, with the four R-21.35 indexes reserve_store.go's ledger reads,
//	the R-21.126 cascade, the R-21.131 share and the R-21.115 fenced
//	sweep each need.
//
// Inputs: none. Outputs: MigrationSet, ApplyReservationSchema.
// Constraints: forward-only, idempotent re-apply, its own SetID so it
//
//	applies and re-applies independently of every other package's own
//	MigrationSet (R-16.77 per-SetID identity).
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableReservation is jobs_reservation, following jobs' own table-prefix
// convention for the closed `jobs` storage domain (R-21.22).
const tableReservation = "jobs_reservation"

// reservationSchemaVersion is this package's own MigrationSet sequence
// number, independent of every other package's (R-16.77).
const reservationSchemaVersion = 1

// ReservationSchemaVersion is reservationSchemaVersion, exported for a future
// composition root's reader-ceiling max(), matching topology.SchemaVersion's
// and jobs.SchemaVersion's own pattern.
const ReservationSchemaVersion = reservationSchemaVersion

// reservationTable returns jobs_reservation's TableDef. Column order
// here must match every scanner in reserve_store.go.
func reservationTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableReservation,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "job_id", Type: migrate.TypeText, NotNull: true},
			{Name: "project_id", Type: migrate.TypeText, NotNull: true},
			{Name: "lane_id", Type: migrate.TypeText, NotNull: true},
			{Name: "domain_id", Type: migrate.TypeText, NotNull: true},
			{Name: "scope_id", Type: migrate.TypeText, NotNull: true},
			{Name: "kind", Type: migrate.TypeText, NotNull: true},
			{Name: "estimate_json", Type: migrate.TypeText, NotNull: true},
			{Name: "actual_json", Type: migrate.TypeText, NotNull: true},
			{Name: "actual_source", Type: migrate.TypeText, NotNull: true},
			{Name: "base_price", Type: migrate.TypeReal, NotNull: true},
			{Name: "price_table_version", Type: migrate.TypeText, NotNull: true},
			{Name: "scarce_units", Type: migrate.TypeReal, NotNull: true},
			{Name: "permit_id", Type: migrate.TypeText, NotNull: true},
			{Name: "worktree_id", Type: migrate.TypeText, NotNull: true},
			{Name: "lease_ids_json", Type: migrate.TypeText, NotNull: true},
			{Name: "steps_json", Type: migrate.TypeText, NotNull: true},
			{Name: "state", Type: migrate.TypeText, NotNull: true},
			{Name: "owner_epoch", Type: migrate.TypeText, NotNull: true},
			{Name: "heartbeat_at", Type: migrate.TypeInteger, NotNull: true},
			{Name: "expires_at", Type: migrate.TypeInteger, NotNull: true},
			{Name: "created", Type: migrate.TypeInteger, NotNull: true},
		},
	}
}

func reservationTablePtr() *migrate.TableDef {
	t := reservationTable()
	return &t
}

func reservationDomainStateIndex() *migrate.IndexDef {
	return &migrate.IndexDef{Name: "idx_jobs_reservation_domain_state", Table: tableReservation, Columns: []string{"domain_id", "state"}}
}

func reservationScopeStateIndex() *migrate.IndexDef {
	return &migrate.IndexDef{Name: "idx_jobs_reservation_scope_state", Table: tableReservation, Columns: []string{"scope_id", "state"}}
}

func reservationProjectStateIndex() *migrate.IndexDef {
	return &migrate.IndexDef{Name: "idx_jobs_reservation_project_state", Table: tableReservation, Columns: []string{"project_id", "state"}}
}

func reservationSweepIndex() *migrate.IndexDef {
	return &migrate.IndexDef{Name: "idx_jobs_reservation_sweep", Table: tableReservation, Columns: []string{"state", "owner_epoch", "heartbeat_at"}}
}

// ReservationMigrationSet is the reservation table's schema, inside the EXISTING
// `jobs` storage domain (R-21.22 -- table-prefix convention only, no new
// DomainID).
func ReservationMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "fleet-economics-reservation",
		SchemaVersion: reservationSchemaVersion,
		ReaderCeiling: reservationSchemaVersion,
		Steps: []migrate.MigrationStep{
			{Kind: migrate.StepCreateTable, Table: reservationTablePtr(), Description: "jobs_reservation: the R-21.35 atomic-reservation ledger row"},
			{Kind: migrate.StepCreateIndex, Index: reservationDomainStateIndex(), Description: "quota ledger read by (domain_id, state)"},
			{Kind: migrate.StepCreateIndex, Index: reservationScopeStateIndex(), Description: "R-21.126 cascade read by (scope_id, state)"},
			{Kind: migrate.StepCreateIndex, Index: reservationProjectStateIndex(), Description: "R-21.131 share read by (project_id, state)"},
			{Kind: migrate.StepCreateIndex, Index: reservationSweepIndex(), Description: "R-21.115 fenced sweep read by (state, owner_epoch, heartbeat_at)"},
		},
	}
}

// ApplyReservationSchema idempotently applies MigrationSet against db.
// dbPath/backupDir enable migrate's SQLite snapshot when non-empty.
func ApplyReservationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: ApplyReservationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: ApplyReservationSchema requires a non-nil Clock")
	}
	cfg := migrate.ApplyConfig{DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir}
	return migrate.Apply(ctx, cfg, ReservationMigrationSet())
}
