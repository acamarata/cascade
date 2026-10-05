// Purpose: the rate-limit cascade. When a limit scope is rate limited,
//
//	every held reservation on it is parked: it keeps its quota estimate
//	and its leases but holds no permit until it is unparked.
//
// Inputs: a limit scope id. Outputs: CascadeRateLimited.
// Constraints: committed rows run to their end and are left alone; every
//
//	move goes through the store's transition; one failing row does not
//	stop the others, and every error is returned joined. A park is a
//	state-only compare-and-set, so a Bind that commits between the listing
//	and the park keeps its placement, and it is counted only when that
//	write lands. A row Reserve is still admitting or acquiring (quota,
//	leases, worktree) is not parked mid-way: the park is deferred to the
//	end of that Reserve and is not counted by the cascade. The in-flight
//	mark is cleared by a defer, so a panicking seam cannot leak it.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CascadeRateLimited parks every held reservation on scopeID and returns
// how many parks landed. A row Reserve is still acquiring is parked the
// moment its acquisition finishes, never under its feet, and is not in
// the count: no park of it has landed yet.
func (rv *Reserver) CascadeRateLimited(ctx context.Context, scopeID string) (int, error) {
	if scopeID == "" {
		return 0, cascade.New(cascade.KindInvalidInput, "economics: CascadeRateLimited requires a scope id")
	}
	rows, err := rv.store.activeOnScope(ctx, scopeID, "")
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, r := range rows {
		if r.State != ReservationHeld {
			continue
		}
		parked, err := rv.parkHeld(ctx, r.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if parked {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// parkHeld parks id by a state-only compare-and-set and reports whether
// that write landed; a row that left held meanwhile is not an error.
// While Reserve is still acquiring id it only asks the acquisition to
// park the row when done, and reports false.
func (rv *Reserver) parkHeld(ctx context.Context, id string) (bool, error) {
	rv.acqMu.Lock()
	defer rv.acqMu.Unlock()
	if _, ok := rv.acquiring[id]; ok {
		rv.acquiring[id] = true
		return false, nil
	}
	landed, err := rv.store.casState(ctx, id, ReservationHeld, ReservationParked)
	return landed, err
}

// beginAcquire marks id as in flight: Reserve is still admitting it or
// acquiring its leases and worktree.
func (rv *Reserver) beginAcquire(id string) {
	rv.acqMu.Lock()
	defer rv.acqMu.Unlock()
	rv.acquiring[id] = false
}

// endAcquire is deferred by Reserve. It clears id's in-flight mark on
// every exit, a panicking seam included. After a panic it retires the
// row from its stored step ledger (RecoverSteps, then the reverse
// teardown) and re-panics with the original value; a row that cannot be
// retired is dropped from the in-memory set, so ExpireStale retries it.
func (rv *Reserver) endAcquire(ctx context.Context, id string) {
	rv.acqMu.Lock()
	delete(rv.acquiring, id)
	rv.acqMu.Unlock()
	if p := recover(); p != nil {
		if r, err := rv.mustGet(ctx, id); err == nil && !r.State.Terminal() {
			if rv.recoverAndRetire(ctx, r) != nil {
				rv.untrack(id)
			}
		}
		panic(p)
	}
}

// finishAcquire clears id's acquiring mark and, when a cascade asked for
// the row meanwhile, parks the acquired row now by a state-only
// compare-and-set; a failed park rolls the row back. A failed
// acquisition is returned unchanged.
func (rv *Reserver) finishAcquire(ctx context.Context, id string, r Reservation, err error) (Reservation, error) {
	rv.acqMu.Lock()
	park := rv.acquiring[id]
	delete(rv.acquiring, id)
	if err != nil || !park {
		rv.acqMu.Unlock()
		return r, err
	}
	parked, perr := rv.store.transitionState(ctx, r, ReservationParked)
	rv.acqMu.Unlock()
	if perr != nil {
		return rv.failAndRollback(ctx, r, perr)
	}
	return parked, nil
}
