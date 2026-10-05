// Purpose: the fenced daemon-start Sweep and the periodic ExpireStale,
//
//	the two paths that retire rows no live process can finish, so a
//	killed daemon leaks no capacity, lease or worktree.
//
// Inputs: the active rows, this Reserver's owner epoch and in-memory set,
//
//	an OtherDaemonLiveFn probe and the heartbeat interval.
//
// Outputs: Sweep (start only) and ExpireStale (every interval).
// Constraints: Sweep refuses (ErrConcurrentDaemon, nothing written) while
//
//	another daemon is live on the same home. Its owner then being dead,
//	every foreign-epoch held/parked row is recovered (RecoverSteps) and
//	rolled back at once; a foreign committed row is adopted (epoch
//	rewritten, heartbeat refreshed) but NOT held in memory, so
//	ExpireStale releases it after three intervals unless re-admission
//	claims it with Adopt. ExpireStale never touches a row this process
//	holds: its own rows retire only through Commit/Release/rollback, and
//	a row it has started retiring cannot be adopted (claimExpiry). Before
//	any recovery or teardown, the row is fenced in the database: its
//	owner_epoch moves to this epoch only while epoch, heartbeat and state
//	are as listed, so a row another process adopted, renewed or claimed
//	since the listing is skipped, and a later Adopt by that process is
//	refused (the row is no longer its epoch). A
//	recovery lookup error leaves the row as it is and is counted and
//	returned, never treated as success.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SweepReport counts what one Sweep did.
type SweepReport struct {
	RolledBack, Adopted, Expired, RollbackErrors int
}

// OtherDaemonLiveFn reports whether another daemon is live on the same
// home.
type OtherDaemonLiveFn func(ctx context.Context) (bool, error)

// Sweep runs once at daemon start; see this file's Constraints.
func (rv *Reserver) Sweep(ctx context.Context, live OtherDaemonLiveFn) (SweepReport, error) {
	if live == nil {
		return SweepReport{}, cascade.New(cascade.KindInvalidInput, "economics: Sweep requires an OtherDaemonLiveFn")
	}
	isLive, err := live(ctx)
	if err != nil {
		return SweepReport{}, err
	}
	if isLive {
		return SweepReport{}, cascade.Wrap(cascade.KindConflict, ErrConcurrentDaemon, "economics: sweep refused")
	}
	rows, err := rv.store.listActive(ctx)
	if err != nil {
		return SweepReport{}, err
	}
	now := rv.clock.Now().Unix()
	var rep SweepReport
	var errs []error
	for _, r := range rows {
		counter, err := rv.sweepOne(ctx, r, now, &rep)
		if err != nil {
			rep.RollbackErrors++
			errs = append(errs, err)
			continue
		}
		if counter != nil {
			*counter++
		}
	}
	return rep, errors.Join(errs...)
}

// sweepOne resolves one active row at start and returns the report
// counter it moved, or nil when the row is this epoch's own.
func (rv *Reserver) sweepOne(ctx context.Context, r Reservation, now int64, rep *SweepReport) (*int, error) {
	switch {
	case r.Kind == ReservationBatch && r.ExpiresAt > 0 && r.ExpiresAt <= now:
		return &rep.Expired, rv.recoverAndRetire(ctx, r)
	case r.OwnerEpoch == rv.ownerEpoch:
		return nil, nil
	case r.State == ReservationCommitted:
		ok, err := rv.store.adoptEpoch(ctx, r.ID, r.OwnerEpoch, rv.ownerEpoch, now)
		if err == nil && !ok {
			err = cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "reservation %q changed while adopting", r.ID)
		}
		return &rep.Adopted, err
	default:
		return &rep.RolledBack, rv.recoverAndRetire(ctx, r)
	}
}

// recoverAndRetire completes the row's step ledger from the subsystems,
// then tears it down to its terminal state. A lookup error leaves the
// row untouched.
func (rv *Reserver) recoverAndRetire(ctx context.Context, r Reservation) error {
	rec, err := rv.RecoverSteps(ctx, r.ID)
	if err != nil {
		return err
	}
	if rec.State.Terminal() {
		return nil
	}
	_, err = rv.retire(ctx, rec, terminalFor(rec.State))
	return err
}

// ExpireStale retires every active row NOT held in memory whose
// heartbeat_at is older than 3 x interval: held/parked -> rolled_back,
// committed -> released, each after RecoverSteps and with reverse
// compensation. It returns the number retired and every error joined.
func (rv *Reserver) ExpireStale(ctx context.Context, interval time.Duration) (int, error) {
	if interval <= 0 {
		return 0, cascade.Newf(cascade.KindInvalidInput, "economics: ExpireStale needs a positive interval, got %s", interval)
	}
	rows, err := rv.store.listActive(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := rv.clock.Now().Add(-3 * interval).Unix()
	n := 0
	var errs []error
	for _, r := range rows {
		if r.HeartbeatAt >= cutoff {
			continue
		}
		claimed, err := rv.claimExpiry(ctx, r)
		if err != nil {
			errs = append(errs, err)
		}
		if !claimed {
			continue
		}
		err = rv.recoverAndRetire(ctx, r)
		rv.dropExpiry(r.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// claimExpiry marks r as being retired unless this process holds it,
// another ExpireStale of this process is already retiring it, or the
// database fence fails. Adopt checks the same mark under the same mutex,
// so an in-process Adopt either lands first (the row is tracked and
// skipped here) or is refused; another process is ordered by the fence.
func (rv *Reserver) claimExpiry(ctx context.Context, r Reservation) (bool, error) {
	rv.claimMu.Lock()
	defer rv.claimMu.Unlock()
	if _, busy := rv.expiring[r.ID]; busy || rv.isTracked(r.ID) {
		return false, nil
	}
	fenced, err := rv.store.fenceStale(ctx, r, rv.ownerEpoch)
	if err != nil || !fenced {
		return false, err
	}
	rv.expiring[r.ID] = struct{}{}
	return true, nil
}

// fenceStale claims r's row for retirement by this epoch: owner_epoch
// moves to epoch only while owner_epoch, heartbeat_at and state are still
// the values r was listed with. false means the row changed since.
func (s *ReservationStore) fenceStale(ctx context.Context, r Reservation, epoch string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableReservation+` SET owner_epoch = ? WHERE id = ? AND owner_epoch = ? AND heartbeat_at = ? AND state = ?`,
		epoch, r.ID, r.OwnerEpoch, r.HeartbeatAt, string(r.State))
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: fence stale reservation")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: fence stale reservation")
	}
	return n == 1, nil
}

// dropExpiry clears the mark claimExpiry set.
func (rv *Reserver) dropExpiry(id string) {
	rv.claimMu.Lock()
	defer rv.claimMu.Unlock()
	delete(rv.expiring, id)
}
