// Purpose: the R-21.22 `reservation` table (`jobs_reservation`, the
//
//	`jobs` storage domain's table-prefix convention -- R-14.5/R-16.51's
//	closed domain list gains no new member) through the B/S-02.T3 typed
//	DSL, with the indexes the ledger reads, the cascade, the project
//	share, the fenced sweep and the execution lookup each need, plus the
//	row codec every store read and write shares.
//
// v1 is amended in place (placement columns, repo_id, scope_globs and
//
//	the unique execution_id index): no database had this table outside
//	tests, and the migrate DSL has no ALTER. A ledger holding the old v1
//	checksums fails loudly with the migrate checksum conflict.
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
	"encoding/json"
	"strings"

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
			{Name: "execution_id", Type: migrate.TypeText, NotNull: true},
			{Name: "job_id", Type: migrate.TypeText, NotNull: true},
			{Name: "project_id", Type: migrate.TypeText, NotNull: true},
			{Name: "lane_id", Type: migrate.TypeText, NotNull: true},
			{Name: "domain_id", Type: migrate.TypeText, NotNull: true},
			{Name: "scope_id", Type: migrate.TypeText, NotNull: true},
			{Name: "repo_id", Type: migrate.TypeText, NotNull: true},
			{Name: "scope_globs", Type: migrate.TypeText, NotNull: true},
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
			{Name: "node_id", Type: migrate.TypeText, NotNull: true},
			{Name: "selected_tier", Type: migrate.TypeText, NotNull: true},
			{Name: "sensitivity", Type: migrate.TypeText, NotNull: true},
			{Name: "decision_id", Type: migrate.TypeText, NotNull: true},
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

func reservationExecutionIndex() *migrate.IndexDef {
	return &migrate.IndexDef{Name: "idx_jobs_reservation_execution", Table: tableReservation, Columns: []string{"execution_id"}, Unique: true}
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
			{Kind: migrate.StepCreateIndex, Index: reservationExecutionIndex(), Description: "one reservation per execution, read by execution_id"},
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

// reservationColumns is the column list in table order; every scanner and
// rowArgs follow it.
const reservationColumns = `id, execution_id, job_id, project_id, lane_id, domain_id, scope_id, repo_id, scope_globs, kind,
	estimate_json, actual_json, actual_source, base_price, price_table_version, scarce_units,
	permit_id, worktree_id, lease_ids_json, steps_json, state, owner_epoch, heartbeat_at, expires_at, created,
	node_id, selected_tier, sensitivity, decision_id`

// ownedElsewhere are the columns a progress write never touches: the id,
// the placement (Bind's alone) and liveness (adoptEpoch, fenceStale and
// touchHeartbeat's alone). A stale in-memory row can therefore never
// erase a placement or a claim that landed after it was read.
var ownedElsewhere = map[string]bool{
	"id": true, "node_id": true, "selected_tier": true, "sensitivity": true, "decision_id": true,
	"owner_epoch": true, "heartbeat_at": true,
}

// reservationPlaceholders, progressSet and progressIdx (the rowArgs
// positions progressSet binds) are derived from reservationColumns so
// they can never disagree.
var reservationPlaceholders, progressSet, progressIdx = func() (string, string, []int) {
	cols := strings.Split(reservationColumns, ",")
	marks := make([]string, len(cols))
	var sets []string
	var idx []int
	for i, c := range cols {
		marks[i] = "?"
		if c = strings.TrimSpace(c); !ownedElsewhere[c] {
			sets = append(sets, c+" = ?")
			idx = append(idx, i)
		}
	}
	return strings.Join(marks, ","), strings.Join(sets, ", "), idx
}()

// rowArgs returns r's column values in reservationColumns order.
func rowArgs(r Reservation) ([]any, error) {
	enc := make([]string, 5)
	for i, v := range []any{r.ScopeGlobs, r.Estimate, r.Actual, r.LeaseIDs, r.Steps} {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "economics: encode reservation")
		}
		enc[i] = string(b)
	}
	return []any{
		r.ID, r.ExecutionID, r.JobID, r.ProjectID, r.LaneID, r.DomainID, r.ScopeID, r.RepoID, enc[0], string(r.Kind),
		enc[1], enc[2], string(r.ActualSource), r.BasePrice, r.PriceTableVersion, r.ScarceUnits,
		r.PermitID, r.WorktreeID, enc[3], enc[4], string(r.State), r.OwnerEpoch, r.HeartbeatAt, r.ExpiresAt, r.Created,
		r.NodeID, r.SelectedTier, r.Sensitivity, r.DecisionID,
	}, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanReservation reads one row in reservationColumns order.
func scanReservation(row rowScanner) (Reservation, error) {
	var r Reservation
	var globs, kind, est, act, leases, steps, state string
	if err := row.Scan(&r.ID, &r.ExecutionID, &r.JobID, &r.ProjectID, &r.LaneID, &r.DomainID, &r.ScopeID, &r.RepoID, &globs, &kind,
		&est, &act, &r.ActualSource, &r.BasePrice, &r.PriceTableVersion, &r.ScarceUnits,
		&r.PermitID, &r.WorktreeID, &leases, &steps, &state, &r.OwnerEpoch, &r.HeartbeatAt, &r.ExpiresAt, &r.Created,
		&r.NodeID, &r.SelectedTier, &r.Sensitivity, &r.DecisionID,
	); err != nil {
		return Reservation{}, err
	}
	r.Kind, r.State = ReservationKind(kind), ReservationState(state)
	for _, d := range []struct {
		raw string
		dst any
	}{{globs, &r.ScopeGlobs}, {est, &r.Estimate}, {act, &r.Actual}, {leases, &r.LeaseIDs}, {steps, &r.Steps}} {
		if err := json.Unmarshal([]byte(d.raw), d.dst); err != nil {
			return Reservation{}, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode reservation")
		}
	}
	return r, nil
}
