// Purpose: the transactional reconcile lane writer that feeds the lane
// health observer (P1-TOP-11). UpsertLanes stages every lane write in one
// transaction, records each health change of an existing lane, and calls the
// observer only after Commit returns nil. A rolled-back transaction, or an
// unchanged health, publishes nothing.

package topology

import (
	"context"
	"errors"
)

// UpsertLanes writes recs through UpsertLane inside one transaction. After a
// successful commit it calls the bound LaneHealthObserver once per existing
// lane whose health changed, with (lane, from, to). A new lane is an insert,
// not a transition, and publishes nothing.
func (s *Store) UpsertLanes(ctx context.Context, recs []Lane) error {
	var staged []HealthTransition
	err := s.withTx(ctx, func(tx *Store) error {
		staged = staged[:0]
		for _, rec := range recs {
			tr, changed, err := upsertLaneTracked(ctx, tx, rec)
			if err != nil {
				return err
			}
			if changed {
				staged = append(staged, tr)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publishHealth(staged)
	return nil
}

// upsertLaneTracked reads rec's prior health, writes rec, and reports a
// transition when an existing lane's health differs.
func upsertLaneTracked(ctx context.Context, tx *Store, rec Lane) (HealthTransition, bool, error) {
	prior, err := tx.GetLane(ctx, rec.ID)
	existed := err == nil
	if err != nil && !errors.Is(err, ErrTopologyNotFound) {
		return HealthTransition{}, false, err
	}
	if err := tx.UpsertLane(ctx, rec); err != nil {
		return HealthTransition{}, false, err
	}
	if !existed || prior.Health == rec.Health {
		return HealthTransition{}, false, nil
	}
	return HealthTransition{Lane: rec.ID, From: prior.Health, To: rec.Health}, true, nil
}
