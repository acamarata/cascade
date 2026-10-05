// Purpose: Bind, the one transaction that writes a held reservation's
//
//	placement (node, tier, sensitivity, decision) together with the
//	caller's own rows (its execution and scheduler-decision rows), so a
//	placement never exists without the rows that explain it.
//
// Inputs: a held reservation id, a complete Placement and a BindFn that
//
//	writes the caller's rows on the same *sql.Tx.
//
// Outputs: the bound row, or the rolled-back row and the cause.
// Constraints: one tx on the store's db. The placement write, the BindFn
//
//	and the commit either all land or none does; any failure rolls the
//	tx back and then runs the reverse teardown to rolled_back, returning
//	errors.Join(cause, teardown error). A non-held row is refused and
//	left as it is.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Placement is where and how a reservation runs. Every field is
// required.
type Placement struct {
	NodeID       string
	SelectedTier string
	Sensitivity  string
	DecisionID   string
}

// validate refuses a placement with any empty field.
func (p Placement) validate() error {
	for name, v := range map[string]string{"NodeID": p.NodeID, "SelectedTier": p.SelectedTier, "Sensitivity": p.Sensitivity, "DecisionID": p.DecisionID} {
		if v == "" {
			return cascade.Newf(cascade.KindInvalidInput, "economics: Placement.%s is required", name)
		}
	}
	return nil
}

// BindFn writes the caller's rows for r inside Bind's transaction.
type BindFn func(ctx context.Context, tx *sql.Tx, r Reservation) error

// Bind re-reads the row inside one transaction (it must be held), writes
// p, runs fn on the same tx and commits. Any failure after the row was
// found rolls the tx back and then rolls the reservation back through
// the reverse teardown.
func (rv *Reserver) Bind(ctx context.Context, id string, p Placement, fn BindFn) (Reservation, error) {
	if err := p.validate(); err != nil {
		return Reservation{}, err
	}
	if fn == nil {
		return Reservation{}, cascade.New(cascade.KindInvalidInput, "economics: Bind requires a non-nil BindFn")
	}
	tx, err := rv.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindUnavailable, err, "economics: begin bind transaction")
	}
	r, ok, err := getOne(ctx, tx, `WHERE id = ?`, id)
	if err == nil && !ok {
		err = cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", id)
	}
	if err == nil && r.State != ReservationHeld {
		err = cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "bind needs a held reservation; %q is %s", id, r.State)
	}
	if err != nil {
		_ = tx.Rollback()
		return Reservation{}, err
	}
	bound, cause := bindInTx(ctx, tx, r, p, fn)
	if cause == nil {
		return bound, nil
	}
	_ = tx.Rollback()
	return rv.failAndRollback(ctx, r, cause)
}

// bindInTx writes the placement, runs fn and commits, returning the
// bound row or the first failure. The tx is not rolled back here.
func bindInTx(ctx context.Context, tx *sql.Tx, r Reservation, p Placement, fn BindFn) (Reservation, error) {
	res, err := tx.ExecContext(ctx, `UPDATE `+tableReservation+` SET node_id = ?, selected_tier = ?, sensitivity = ?, decision_id = ? WHERE id = ? AND state = ?`,
		p.NodeID, p.SelectedTier, p.Sensitivity, p.DecisionID, r.ID, string(ReservationHeld))
	if err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindUnavailable, err, "economics: write placement")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return Reservation{}, cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "placement write matched %d rows (%v)", n, err)
	}
	r.NodeID, r.SelectedTier, r.Sensitivity, r.DecisionID = p.NodeID, p.SelectedTier, p.Sensitivity, p.DecisionID
	if err := fn(ctx, tx, r); err != nil {
		return Reservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Reservation{}, cascade.Wrap(cascade.KindUnavailable, err, "economics: commit bind transaction")
	}
	return r, nil
}
