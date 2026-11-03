// Purpose: Store.ReversePromotion, the bounded and audited reversal of a
//
//	lane promotion. A reversal moves a lane to another class for at most
//	MaxReversalWindow, recording the prior lane_class/base_shadow_price in
//	a config_lane_promotion_reversal row; ApplyPromotions restores those
//	prior values once the reversal expires and closes the row.
//
// Inputs: a ReversalRequest, a Clock and a PromotionAuditor.
// Outputs: the two changed lane columns, one reversal row, one audit event.
// Constraints: one transaction per change; an audit failure rolls it back;
//
//	reversal rows are closed (restored_at), never deleted.
//
// SPORT: fleet/topology/promotion_reversal/ADD (P1-TOP-06).

package topology

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// MaxReversalWindow bounds how long one reversal may hold a lane.
const MaxReversalWindow = 30 * 24 * time.Hour

const tableReversal = "config_lane_promotion_reversal"

const reversalColumns = `id, lane_id, prior_class, prior_price, reversed_class, reason, created_at, expires_at, restored_at`

// ReversalRequest asks that Lane run as Class until Until.
type ReversalRequest struct {
	Lane   LaneID
	Class  LaneClass
	Until  time.Time
	Reason string
}

// Reversal is one persisted reversal row. RestoredAt is nil while active.
type Reversal struct {
	ID            string
	Lane          LaneID
	PriorClass    LaneClass
	PriorPrice    float64
	ReversedClass LaneClass
	Reason        string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	RestoredAt    *time.Time
}

func (s *Store) ensureReversalTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+tableReversal+` (
		id TEXT PRIMARY KEY NOT NULL, lane_id TEXT NOT NULL, prior_class TEXT NOT NULL,
		prior_price REAL NOT NULL, reversed_class TEXT NOT NULL, reason TEXT NOT NULL,
		created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, restored_at INTEGER)`)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "topology: ensure reversal table")
	}
	return nil
}

func reversalRequestErr(req ReversalRequest, clock Clock, audit PromotionAuditor) error {
	bad := func(msg string) error { return cascade.Wrap(cascade.KindInvalidInput, ErrPromotionInput, msg) }
	switch {
	case clock == nil:
		return bad("topology: nil clock")
	case audit == nil:
		return bad("topology: nil promotion auditor")
	case !req.Lane.Valid():
		return bad("topology: reversal names no lane")
	case !req.Class.Valid():
		return bad(fmt.Sprintf("topology: unknown reversal class %q", req.Class))
	}
	now := clock.Now()
	if !req.Until.After(now) {
		return bad("topology: reversal already expired")
	}
	if req.Until.Sub(now) > MaxReversalWindow {
		return bad("topology: reversal longer than 30 days")
	}
	return nil
}

// ReversePromotion moves req.Lane to req.Class (at its R-21.30 base price)
// until req.Until, recording the prior values and one audit event in one
// transaction. Any refusal, read failure or audit failure leaves the lane
// columns and the reversal rows unchanged.
func (s *Store) ReversePromotion(ctx context.Context, req ReversalRequest, clock Clock, audit PromotionAuditor) error {
	if err := reversalRequestErr(req, clock, audit); err != nil {
		return err
	}
	price, err := BaseShadowPrice(req.Class)
	if err != nil {
		return err
	}
	now := clock.Now()
	return s.withTx(ctx, func(tx *Store) error {
		if err := tx.ensureReversalTable(ctx); err != nil {
			return err
		}
		lane, err := tx.GetLane(ctx, req.Lane)
		if err != nil {
			return err
		}
		if err := tx.reversalConflict(ctx, lane, req.Class); err != nil {
			return err
		}
		if err := tx.setLaneClass(ctx, lane.ID, lane.LaneClass, req.Class, price); err != nil {
			return err
		}
		id := fmt.Sprintf("%s@%d", lane.ID, now.UnixMilli())
		_, err = tx.db.ExecContext(ctx, `INSERT INTO `+tableReversal+` (`+reversalColumns+`) VALUES (?,?,?,?,?,?,?,?,NULL)`,
			id, string(lane.ID), string(lane.LaneClass), lane.BaseShadowPrice, string(req.Class), req.Reason,
			now.UnixMilli(), req.Until.UnixMilli())
		if err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "topology: record reversal of lane %q", lane.ID)
		}
		ev := PromotionEvent{Kind: PromotionEventReversed, Lane: lane.ID, FromClass: lane.LaneClass, ToClass: req.Class,
			FromPrice: lane.BaseShadowPrice, ToPrice: price, At: now, ExpiresAt: req.Until, Reason: req.Reason}
		if err := audit.RecordPromotion(ctx, ev); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "topology: audit reversal of lane %q", lane.ID)
		}
		return nil
	})
}

// reversalConflict refuses a retired lane, a no-op class and a lane that
// already has an active reversal.
func (s *Store) reversalConflict(ctx context.Context, lane Lane, class LaneClass) error {
	conflict := func(format string) error {
		return cascade.Wrapf(cascade.KindConflict, ErrTopologyConflict, format, lane.ID)
	}
	if lane.RetiredAt != nil {
		return conflict("lane %q is retired")
	}
	if lane.LaneClass == class {
		return conflict("lane %q already has the requested class")
	}
	held, err := s.activeReversalLanes(ctx)
	if err != nil {
		return err
	}
	if held[lane.ID] {
		return conflict("lane %q already has an active reversal")
	}
	return nil
}

// ListReversals returns every reversal row for lane, oldest first.
func (s *Store) ListReversals(ctx context.Context, lane LaneID) ([]Reversal, error) {
	if err := s.ensureReversalTable(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+reversalColumns+` FROM `+tableReversal+` WHERE lane_id = ? ORDER BY created_at, id`, string(lane))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: list reversals")
	}
	return collectReversals(rows)
}

func (s *Store) activeReversals(ctx context.Context) ([]Reversal, error) {
	if err := s.ensureReversalTable(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+reversalColumns+` FROM `+tableReversal+` WHERE restored_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: list active reversals")
	}
	return collectReversals(rows)
}

func (s *Store) activeReversalLanes(ctx context.Context) (map[LaneID]bool, error) {
	active, err := s.activeReversals(ctx)
	if err != nil {
		return nil, err
	}
	held := make(map[LaneID]bool, len(active))
	for _, r := range active {
		held[r.Lane] = true
	}
	return held, nil
}

func collectReversals(rows *sql.Rows) ([]Reversal, error) {
	defer func() { _ = rows.Close() }()
	out := make([]Reversal, 0)
	for rows.Next() {
		var r Reversal
		var prior, reversed string
		var created, expires int64
		var restored sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Lane, &prior, &r.PriorPrice, &reversed, &r.Reason, &created, &expires, &restored); err != nil {
			return nil, wrapScan(err, "promotion_reversal")
		}
		r.PriorClass, r.ReversedClass = LaneClass(prior), LaneClass(reversed)
		r.CreatedAt, r.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
		if restored.Valid {
			t := time.UnixMilli(restored.Int64)
			r.RestoredAt = &t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: read reversals")
	}
	return out, nil
}

// expireReversals restores the prior class and price of every active
// reversal whose expiry is at or before now, closes its row and audits it.
func (s *Store) expireReversals(ctx context.Context, now time.Time, audit PromotionAuditor, rep *PromotionReport) error {
	active, err := s.activeReversals(ctx)
	if err != nil {
		return err
	}
	for _, r := range active {
		if r.ExpiresAt.After(now) {
			continue
		}
		if !r.PriorClass.Valid() {
			return newInvariantErr("promotion_reversal", r.ID, "unknown prior class")
		}
		if err := s.setLaneClass(ctx, r.Lane, r.ReversedClass, r.PriorClass, r.PriorPrice); err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `UPDATE `+tableReversal+` SET restored_at = ? WHERE id = ? AND restored_at IS NULL`,
			now.UnixMilli(), r.ID)
		if err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "topology: close reversal %q", r.ID)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return cascade.Wrapf(cascade.KindConflict, ErrTopologyConflict, "reversal %q already closed", r.ID)
		}
		ev := PromotionEvent{Kind: PromotionEventReversalExpiry, Lane: r.Lane, FromClass: r.ReversedClass, ToClass: r.PriorClass,
			FromPrice: 0, ToPrice: r.PriorPrice, At: now, ExpiresAt: r.ExpiresAt, Reason: r.Reason}
		if price, perr := BaseShadowPrice(r.ReversedClass); perr == nil {
			ev.FromPrice = price
		}
		if err := audit.RecordPromotion(ctx, ev); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "topology: audit reversal expiry of lane %q", r.Lane)
		}
		rep.Restored = append(rep.Restored, r.Lane)
		rep.RowsChanged += 2
	}
	return nil
}
