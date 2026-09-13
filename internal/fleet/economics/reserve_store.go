// Purpose: ReservationStore, jobs_reservation's typed CRUD surface:
//
//	Insert (a held row, on the injected clock), Get, ListByState,
//	OutstandingHeld (the R-21.114 derived-availability subtrahend) and
//	Transition (the ONLY exported mutator of Reservation.State, gated by
//	TransitionAllowed).
//
// Inputs: an open *sql.DB already migrated via ApplyReservationSchema.
// Outputs: typed taxonomy errors for malformed, missing,
//
//	illegal-transition or storage-failure paths.
//
// Constraints: every write goes through database/sql over the B/S-02
//
//	table (never a second, hand-rolled connection); Transition is the
//	ONLY path that changes State (matches jobs.Store.PutTransition's own
//	precedent).
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReservationStore is jobs_reservation's persisted CRUD surface.
type ReservationStore struct {
	db    *sql.DB
	clock Clock
}

// NewReservationStore wraps db (already migrated via
// ApplyReservationSchema) with clock as the source of Insert's
// `created` timestamp.
func NewReservationStore(db *sql.DB, clock Clock) (*ReservationStore, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReservationStore requires a non-nil db")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReservationStore requires a non-nil Clock")
	}
	return &ReservationStore{db: db, clock: clock}, nil
}

// Insert persists r as a new row. r.Created is stamped from the store's
// clock, overwriting any caller-supplied value, so a crash immediately
// after Insert always leaves a row whose Created reflects when it was
// actually durable.
func (s *ReservationStore) Insert(ctx context.Context, r Reservation) (Reservation, error) {
	if r.ID == "" {
		return Reservation{}, cascade.New(cascade.KindInvalidInput, "economics: reservation id is required")
	}
	if !r.Kind.Valid() {
		return Reservation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationKind, "%q", string(r.Kind))
	}
	if !r.State.Valid() {
		return Reservation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationState, "%q", string(r.State))
	}
	r.Created = s.clock.Now().Unix()
	if err := s.put(ctx, s.db, r, true); err != nil {
		return Reservation{}, err
	}
	return r, nil
}

// put inserts (insertOnly true) or upserts r into jobs_reservation over
// exec (either the store's *sql.DB or a transaction).
func (s *ReservationStore) put(ctx context.Context, exec execer, r Reservation, insertOnly bool) error {
	estJSON, err := json.Marshal(r.Estimate)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "economics: encode estimate")
	}
	actJSON, err := json.Marshal(r.Actual)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "economics: encode actual")
	}
	leaseJSON, err := json.Marshal(r.LeaseIDs)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "economics: encode lease_ids")
	}
	stepsJSON, err := json.Marshal(r.Steps)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "economics: encode steps")
	}
	query := `INSERT INTO ` + tableReservation + ` (id, job_id, project_id, lane_id, domain_id, scope_id, kind,
		estimate_json, actual_json, actual_source, base_price, price_table_version, scarce_units,
		permit_id, worktree_id, lease_ids_json, steps_json, state, owner_epoch, heartbeat_at, expires_at, created)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if !insertOnly {
		query = `INSERT OR REPLACE INTO ` + tableReservation + ` (id, job_id, project_id, lane_id, domain_id, scope_id, kind,
			estimate_json, actual_json, actual_source, base_price, price_table_version, scarce_units,
			permit_id, worktree_id, lease_ids_json, steps_json, state, owner_epoch, heartbeat_at, expires_at, created)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	}
	_, err = exec.ExecContext(ctx, query,
		r.ID, r.JobID, r.ProjectID, r.LaneID, r.DomainID, r.ScopeID, string(r.Kind),
		string(estJSON), string(actJSON), string(r.ActualSource), r.BasePrice, r.PriceTableVersion, r.ScarceUnits,
		r.PermitID, r.WorktreeID, string(leaseJSON), string(stepsJSON), string(r.State), r.OwnerEpoch, r.HeartbeatAt, r.ExpiresAt, r.Created,
	)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: persist reservation")
	}
	return nil
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanReservation(row rowScanner) (Reservation, error) {
	var r Reservation
	var kind, estJSON, actJSON, leaseJSON, stepsJSON, state string
	if err := row.Scan(&r.ID, &r.JobID, &r.ProjectID, &r.LaneID, &r.DomainID, &r.ScopeID, &kind,
		&estJSON, &actJSON, &r.ActualSource, &r.BasePrice, &r.PriceTableVersion, &r.ScarceUnits,
		&r.PermitID, &r.WorktreeID, &leaseJSON, &stepsJSON, &state, &r.OwnerEpoch, &r.HeartbeatAt, &r.ExpiresAt, &r.Created,
	); err != nil {
		return Reservation{}, err
	}
	r.Kind = ReservationKind(kind)
	r.State = ReservationState(state)
	if err := json.Unmarshal([]byte(estJSON), &r.Estimate); err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode estimate")
	}
	if err := json.Unmarshal([]byte(actJSON), &r.Actual); err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode actual")
	}
	if err := json.Unmarshal([]byte(leaseJSON), &r.LeaseIDs); err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode lease_ids")
	}
	if err := json.Unmarshal([]byte(stepsJSON), &r.Steps); err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode steps")
	}
	return r, nil
}

const reservationColumns = `id, job_id, project_id, lane_id, domain_id, scope_id, kind,
	estimate_json, actual_json, actual_source, base_price, price_table_version, scarce_units,
	permit_id, worktree_id, lease_ids_json, steps_json, state, owner_epoch, heartbeat_at, expires_at, created`

// Get returns the reservation with id, or ok=false if none exists.
func (s *ReservationStore) Get(ctx context.Context, id string) (Reservation, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+reservationColumns+` FROM `+tableReservation+` WHERE id = ?`, id)
	r, err := scanReservation(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Reservation{}, false, nil
		}
		return Reservation{}, false, cascade.Wrap(cascade.KindUnavailable, err, "economics: get reservation")
	}
	return r, true, nil
}

// ListByState returns every reservation currently in state.
func (s *ReservationStore) ListByState(ctx context.Context, state ReservationState) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reservationColumns+` FROM `+tableReservation+` WHERE state = ? ORDER BY created ASC`, string(state))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "economics: list reservations by state")
	}
	defer func() { _ = rows.Close() }()
	var out []Reservation
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "economics: scan reservation")
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "economics: iterate reservations")
	}
	return out, nil
}

// OutstandingHeld sums the Estimate (tokens_in + tokens_out + requests,
// this package's single combined-weight admission dimension) of every
// held, parked and committed reservation on domainID -- the R-21.114
// subtrahend a caller nets against a bucket's observed capacity. Buckets
// themselves are never mutated here.
func (s *ReservationStore) OutstandingHeld(ctx context.Context, domainID string) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT estimate_json FROM `+tableReservation+` WHERE domain_id = ? AND state IN (?, ?, ?)`,
		domainID, string(ReservationHeld), string(ReservationParked), string(ReservationCommitted))
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "economics: sum outstanding held")
	}
	defer func() { _ = rows.Close() }()
	var total int64
	for rows.Next() {
		var estJSON string
		if err := rows.Scan(&estJSON); err != nil {
			return 0, cascade.Wrap(cascade.KindIntegrity, err, "economics: scan estimate")
		}
		var est Estimate
		if err := json.Unmarshal([]byte(estJSON), &est); err != nil {
			return 0, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode estimate")
		}
		total += est.TokensIn + est.TokensOut + est.Requests
	}
	if err := rows.Err(); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "economics: iterate outstanding held")
	}
	return total, nil
}

// outstandingHeldExcept sums the same Estimate weight as OutstandingHeld
// but excludes excludeID -- the admission check's own just-inserted row
// -- so a reservation never counts itself as competing outstanding
// demand.
func (s *ReservationStore) outstandingHeldExcept(ctx context.Context, domainID, excludeID string) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT estimate_json FROM `+tableReservation+` WHERE domain_id = ? AND id != ? AND state IN (?, ?, ?)`,
		domainID, excludeID, string(ReservationHeld), string(ReservationParked), string(ReservationCommitted))
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "economics: sum outstanding held")
	}
	defer func() { _ = rows.Close() }()
	var total int64
	for rows.Next() {
		var estJSON string
		if err := rows.Scan(&estJSON); err != nil {
			return 0, cascade.Wrap(cascade.KindIntegrity, err, "economics: scan estimate")
		}
		var est Estimate
		if err := json.Unmarshal([]byte(estJSON), &est); err != nil {
			return 0, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode estimate")
		}
		total += est.TokensIn + est.TokensOut + est.Requests
	}
	if err := rows.Err(); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "economics: iterate outstanding held")
	}
	return total, nil
}

// Transition moves the reservation with id from its current state to
// `to`, refusing with ErrReservationTransition for any move outside the
// legal table. Not found is reported distinctly from an illegal move.
func (s *ReservationStore) Transition(ctx context.Context, id string, to ReservationState) (Reservation, error) {
	if !to.Valid() {
		return Reservation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationState, "%q", string(to))
	}
	r, ok, err := s.Get(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if !ok {
		return Reservation{}, cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", id)
	}
	if !TransitionAllowed(r.State, to) {
		return Reservation{}, cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "%s -> %s", r.State, to)
	}
	r.State = to
	if err := s.put(ctx, s.db, r, false); err != nil {
		return Reservation{}, err
	}
	return r, nil
}

// Replace persists r's current in-memory state verbatim (used by
// reserve.go to record step-ledger progress and lease/worktree ids
// without going through Transition's state-machine gate).
func (s *ReservationStore) Replace(ctx context.Context, r Reservation) error {
	return s.put(ctx, s.db, r, false)
}
