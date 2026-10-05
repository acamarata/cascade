// Purpose: liveness for the reservations this process holds. Heartbeat
//
//	renews heartbeat_at and the repo leases of every held id; a lease
//	the lease table refuses as Conflict (fenced, or past ttl plus grace)
//	means the reservation is lost, so it is torn down, dropped from the
//	in-memory set and reported through the attention seam. A reservation
//	fenced by another process is untracked without renewal or teardown.
//	Adopt claims a row for this process.
//
// Inputs: the in-memory set, the RenewLeases and RaiseAttention seams.
// Outputs: Heartbeat, Adopt.
// Constraints: Heartbeat processes every id and never blocks on one. Its
//
//	error is non-nil only when a heartbeat_at write, the retirement
//	write of a lost reservation, or a RaiseAttention call fails; a fenced
//	reservation is untracked without an error, and a lease
//	refusal is counted, never returned, and a renewal that failed for
//	another reason is left out of renewed.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Heartbeat renews every reservation this process holds and retires the
// ones whose leases were lost. A row fenced by another process is
// untracked without renewal, teardown or error. renewed counts rows whose
// heartbeat and leases were renewed; lostLeases counts rows retired as lost. A row
// whose lease renewal failed with any other kind is in neither count: it
// stays held and tracked and the next tick retries it.
func (rv *Reserver) Heartbeat(ctx context.Context) (renewed, lostLeases int, err error) {
	var errs []error
	for _, id := range rv.trackedIDs() {
		leases, ok, werr := rv.store.touchHeartbeat(ctx, id, rv.ownerEpoch, rv.clock.Now().Unix())
		if werr != nil {
			if hasReservationFencedIdentity(werr) {
				rv.untrack(id)
				continue
			}
			errs = append(errs, werr)
			continue
		}
		if !ok {
			rv.untrack(id) // terminal or gone: nothing left to hold
			continue
		}
		if len(leases) > 0 {
			rerr := rv.seams.RenewLeases(ctx, leases)
			if cascade.HasKind(rerr, cascade.KindConflict) {
				lostLeases++
				errs = append(errs, rv.retireLost(ctx, id)...)
				continue
			}
			if rerr != nil {
				continue // not renewed, not lost: the next tick retries
			}
		}
		renewed++
	}
	return renewed, lostLeases, errors.Join(errs...)
}

// retireLost rolls a lost reservation back, then raises one lost_lease
// attention even when the rollback reported an error.
func (rv *Reserver) retireLost(ctx context.Context, id string) []error {
	var errs []error
	r, err := rv.mustGet(ctx, id)
	if err == nil {
		_, err = rv.retire(ctx, r, terminalFor(r.State))
	}
	if err != nil {
		rv.untrack(id)
		errs = append(errs, err)
	}
	if err := rv.seams.RaiseAttention(ctx, id, AttentionLostLease); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// Adopt claims id for this process: compare-and-set of owner_epoch to
// this epoch with a fresh heartbeat, then adds it to the in-memory set.
// A terminal row, a row another live epoch owns, or a row ExpireStale is
// already retiring is refused.
func (rv *Reserver) Adopt(ctx context.Context, id string) error {
	rv.claimMu.Lock()
	defer rv.claimMu.Unlock()
	if _, ok := rv.expiring[id]; ok {
		return cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "cannot adopt %q: it is being expired", id)
	}
	r, err := rv.mustGet(ctx, id)
	if err != nil {
		return err
	}
	if r.State.Terminal() {
		return cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "cannot adopt %q: it is %s", id, r.State)
	}
	if r.OwnerEpoch != rv.ownerEpoch {
		return cascade.Newf(cascade.KindConflict, "economics: reservation %q is held by live epoch %q", id, r.OwnerEpoch)
	}
	ok, err := rv.store.adoptEpoch(ctx, id, r.OwnerEpoch, rv.ownerEpoch, rv.clock.Now().Unix())
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "reservation %q changed while adopting", id)
	}
	rv.track(id)
	return nil
}

// hasReservationFencedIdentity checks the sentinel itself, not its Kind:
// cascade.Error.Is deliberately considers all errors of a Kind equal.
func hasReservationFencedIdentity(err error) bool {
	for err != nil {
		if err == ErrReservationFenced {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// touchHeartbeat writes heartbeat_at only for the active row's current
// owner epoch and returns its lease ids. A competing fence is a typed
// refusal; terminal or missing rows still return ok=false without error.
func (s *ReservationStore) touchHeartbeat(ctx context.Context, id, epoch string, now int64) (leases []string, ok bool, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx, `UPDATE `+tableReservation+` SET heartbeat_at = ? WHERE id = ? AND owner_epoch = ? AND state IN `+activeStates+` RETURNING lease_ids_json`, now, id, epoch).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s.classifyUnmatchedHeartbeat(ctx, id, epoch)
	}
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindUnavailable, err, "economics: write heartbeat")
	}
	if err := json.Unmarshal([]byte(raw), &leases); err != nil {
		return nil, false, cascade.Wrap(cascade.KindIntegrity, err, "economics: decode lease_ids")
	}
	return leases, true, nil
}

func (s *ReservationStore) classifyUnmatchedHeartbeat(ctx context.Context, id, epoch string) ([]string, bool, error) {
	got, ok, err := s.Get(ctx, id)
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindUnavailable, err, "economics: write heartbeat")
	}
	if !ok || got.State.Terminal() {
		return nil, false, nil
	}
	if got.OwnerEpoch != epoch {
		return nil, false, cascade.Wrapf(cascade.KindConflict, ErrReservationFenced, "economics: reservation %q is fenced by epoch %q", id, got.OwnerEpoch)
	}
	return nil, false, cascade.Newf(cascade.KindUnavailable, "economics: heartbeat of %q matched no row", id)
}

// adoptEpoch rewrites owner_epoch from -> to with a fresh heartbeat on an
// active row still owned by from; ok is false when nothing matched.
func (s *ReservationStore) adoptEpoch(ctx context.Context, id, from, to string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableReservation+` SET owner_epoch = ?, heartbeat_at = ? WHERE id = ? AND owner_epoch = ? AND state IN `+activeStates, to, now, id, from)
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: adopt reservation")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: adopt reservation")
	}
	return n == 1, nil
}
