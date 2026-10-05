// Purpose: ReservationStore, jobs_reservation's typed CRUD surface:
//
//	Insert (a held row, on the injected clock), Get, GetByExecution,
//	ListByState, the active-row reads admission and the sweep use, and
//	Transition (the ONLY path that changes Reservation.State, gated by
//	TransitionAllowed and compare-and-set on the stored state).
//	A pure state move writes the state column alone; a progress write
//	(Replace, retirement) never writes the placement or liveness
//	columns, so a write from a stale read cannot erase a Bind or a claim.
//
// Inputs: an open *sql.DB already migrated via ApplyReservationSchema.
// Outputs: typed taxonomy errors for malformed, missing, duplicate,
//
//	illegal-transition or storage-failure paths.
//
// Constraints: every write goes through database/sql over this one
//
//	table; Replace persists progress only and refuses a state change, so
//	a stale in-memory row can never overwrite a newer state.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReservationStore is jobs_reservation's persisted CRUD surface.
type ReservationStore struct {
	db    *sql.DB
	clock Clock
	// beforeWrite, when set, runs at the start of every state and progress
	// write, after the caller's read. Only tests set it, to land a
	// concurrent write inside that window; it is nil in production.
	beforeWrite func(ctx context.Context, id string)
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

// DB returns the store's handle, so a caller's own rows can be written in
// the same transaction Bind opens.
func (s *ReservationStore) DB() *sql.DB { return s.db }

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Insert persists r as a new row. r.Created is stamped from the store's
// clock. A second row with the same ExecutionID is refused KindConflict
// by the unique index.
func (s *ReservationStore) Insert(ctx context.Context, r Reservation) (Reservation, error) {
	switch {
	case r.ID == "":
		return Reservation{}, cascade.New(cascade.KindInvalidInput, "economics: reservation id is required")
	case r.ExecutionID == "":
		return Reservation{}, cascade.New(cascade.KindInvalidInput, "economics: reservation execution id is required")
	case !r.Kind.Valid():
		return Reservation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationKind, "%q", string(r.Kind))
	case !r.State.Valid():
		return Reservation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationState, "%q", string(r.State))
	}
	r.Created = s.clock.Now().Unix()
	args, err := rowArgs(r)
	if err != nil {
		return Reservation{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+tableReservation+` (`+reservationColumns+`) VALUES (`+reservationPlaceholders+`)`, args...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Reservation{}, cascade.Wrapf(cascade.KindConflict, err, "economics: execution %q already has a reservation", r.ExecutionID)
		}
		return Reservation{}, cascade.Wrap(cascade.KindUnavailable, err, "economics: persist reservation")
	}
	return r, nil
}

// Get returns the reservation with id, or ok=false if none exists.
func (s *ReservationStore) Get(ctx context.Context, id string) (Reservation, bool, error) {
	return getOne(ctx, s.db, `WHERE id = ?`, id)
}

// GetByExecution returns the reservation bound to executionID, or
// ok=false if none exists.
func (s *ReservationStore) GetByExecution(ctx context.Context, executionID string) (Reservation, bool, error) {
	return getOne(ctx, s.db, `WHERE execution_id = ?`, executionID)
}

// getOne reads the single row matching where over q (the db or a tx).
func getOne(ctx context.Context, q execer, where string, arg string) (Reservation, bool, error) {
	r, err := scanReservation(q.QueryRowContext(ctx, `SELECT `+reservationColumns+` FROM `+tableReservation+` `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, cascade.Wrap(cascade.KindUnavailable, err, "economics: get reservation")
	}
	return r, true, nil
}

// ListByState returns every reservation currently in state.
func (s *ReservationStore) ListByState(ctx context.Context, state ReservationState) ([]Reservation, error) {
	return s.list(ctx, `WHERE state = ? ORDER BY created ASC, id ASC`, string(state))
}

// activeStates is the SQL list of states that still hold an estimate.
const activeStates = `('held', 'parked', 'committed')`

// listActive returns every held, parked and committed row.
func (s *ReservationStore) listActive(ctx context.Context) ([]Reservation, error) {
	return s.list(ctx, `WHERE state IN `+activeStates+` ORDER BY created ASC, id ASC`)
}

// activeOnScope returns every held, parked and committed row on scopeID
// except excludeID: the outstanding demand admission nets per dimension.
// Keyed by scope, not domain, so two domains sharing a limit scope see
// each other's reservations.
func (s *ReservationStore) activeOnScope(ctx context.Context, scopeID, excludeID string) ([]Reservation, error) {
	return s.list(ctx, `WHERE scope_id = ? AND id != ? AND state IN `+activeStates+` ORDER BY created ASC, id ASC`, scopeID, excludeID)
}

// list runs one SELECT over every column with the given tail.
func (s *ReservationStore) list(ctx context.Context, tail string, args ...any) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reservationColumns+` FROM `+tableReservation+` `+tail, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "economics: list reservations")
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

// Transition moves the reservation with id to `to`, refusing with
// ErrReservationTransition for any move outside the legal table. Not
// found is reported distinctly from an illegal move.
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
	return s.transitionState(ctx, r, to)
}

// transitionState moves r's row from r.State to `to` by a state-only
// compare-and-set and returns the row as stored. No other column is
// written, so a placement, lease or liveness write that landed after r
// was read survives the move.
func (s *ReservationStore) transitionState(ctx context.Context, r Reservation, to ReservationState) (Reservation, error) {
	if !TransitionAllowed(r.State, to) {
		return Reservation{}, cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "%s -> %s", r.State, to)
	}
	landed, err := s.casState(ctx, r.ID, r.State, to)
	if err != nil {
		return Reservation{}, err
	}
	if !landed {
		return Reservation{}, s.staleState(ctx, r.ID, r.State, to)
	}
	stored, ok, err := s.Get(ctx, r.ID)
	if err == nil && !ok {
		err = cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", r.ID)
	}
	return stored, err
}

// casState sets id's state from -> to and writes nothing else. landed is
// false when the stored state is no longer from.
func (s *ReservationStore) casState(ctx context.Context, id string, from, to ReservationState) (bool, error) {
	if s.beforeWrite != nil {
		s.beforeWrite(ctx, id)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableReservation+` SET state = ? WHERE id = ? AND state = ?`, string(to), id, string(from))
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: persist reservation state")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: persist reservation state")
	}
	return n == 1, nil
}

// transitionRow writes r's progress columns with State `to`,
// compare-and-set on r's current state: the move must be legal and the
// stored state must still be r's. Retirement uses it, so the teardown's
// cleared handles land with the terminal state. The row is returned as
// stored.
func (s *ReservationStore) transitionRow(ctx context.Context, r Reservation, to ReservationState) (Reservation, error) {
	if !TransitionAllowed(r.State, to) {
		return Reservation{}, cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "%s -> %s", r.State, to)
	}
	from := r.State
	r.State = to
	if err := s.update(ctx, r, from); err != nil {
		return Reservation{}, err
	}
	stored, ok, err := s.Get(ctx, r.ID)
	if err == nil && !ok {
		err = cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", r.ID)
	}
	return stored, err
}

// Replace persists r's progress (steps, handles, accounting) without
// changing its state: the stored state must equal r.State. The placement
// and liveness columns are not written (see progressSet).
func (s *ReservationStore) Replace(ctx context.Context, r Reservation) error {
	return s.update(ctx, r, r.State)
}

// update writes r's progress columns (progressSet: never the placement
// or liveness) where the stored state is from.
func (s *ReservationStore) update(ctx context.Context, r Reservation, from ReservationState) error {
	all, err := rowArgs(r)
	if err != nil {
		return err
	}
	args := make([]any, 0, len(progressIdx)+2)
	for _, i := range progressIdx {
		args = append(args, all[i])
	}
	if s.beforeWrite != nil {
		s.beforeWrite(ctx, r.ID)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableReservation+` SET `+progressSet+` WHERE id = ? AND state = ?`,
		append(args, r.ID, string(from))...)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: persist reservation")
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	return s.staleState(ctx, r.ID, from, r.State)
}

// staleState is the refusal of a write that matched no row: KindNotFound
// when id is gone, else ErrReservationTransition naming the stored state.
func (s *ReservationStore) staleState(ctx context.Context, id string, from, to ReservationState) error {
	stored, ok, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", id)
	}
	return cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "reservation %q is %s, not %s (writing %s)", id, stored.State, from, to)
}
