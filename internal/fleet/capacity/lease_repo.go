// Purpose: RepoOfLease, the lookup that answers which repository a
// live lease was issued for, from the in-memory lease index plus the
// reservation's own state.
//
// Inputs: a lease id.
// Outputs: (repoID, true, nil) for a live lease with a repo; ("", false, nil)
// for a live lease issued with no repo; ErrLeaseUnknown for anything else.
// Constraints: the repo is only what the caller put on DispatchRef.RepoID; it
// is never derived from a path, a name, the job or ScopeGlobs. The index is
// in memory and not persisted: after a restart every earlier lease id is
// unknown (fail closed). A lease answers only while its reservation is held or
// committed. No error path returns ("", false, nil): an unknown lease is
// never empty-as-success.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
)

// leaseEntry is what the index remembers about one granted lease.
type leaseEntry struct{ repoID, executionID, reservationID string }

// leaseIndex maps a lease id to its entry under a mutex.
type leaseIndex struct {
	mu      sync.Mutex
	entries map[string]leaseEntry
}

func newLeaseIndex() leaseIndex { return leaseIndex{entries: map[string]leaseEntry{}} }

// put records a granted lease.
func (x *leaseIndex) put(id string, e leaseEntry) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.entries[id] = e
}

// get returns the entry for a lease id.
func (x *leaseIndex) get(id string) (leaseEntry, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	e, ok := x.entries[id]
	return e, ok
}

// drop forgets a lease id.
func (x *leaseIndex) drop(id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.entries, id)
}

// all returns a copy of every entry, so a caller can read the ledger without
// holding the index lock.
func (x *leaseIndex) all() map[string]leaseEntry {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make(map[string]leaseEntry, len(x.entries))
	for id, e := range x.entries {
		out[id] = e
	}
	return out
}

// RepoOfLease reports the repository leaseID was issued for. ok is true only
// with a non-empty repoID.
func (f *Fleet) RepoOfLease(ctx context.Context, leaseID string) (repoID string, ok bool, err error) {
	if leaseID == "" {
		return "", false, cascade.New(cascade.KindInvalidInput, "capacity: RepoOfLease requires a lease id")
	}
	e, known := f.index.get(leaseID)
	if !known {
		return "", false, refuse(ErrLeaseUnknown, nil, "lease %q", leaseID)
	}
	r, found, err := f.deps.Reservations.GetByExecution(ctx, e.executionID)
	if err != nil {
		return "", false, err
	}
	if !liveFor(e, r, found) {
		if retired(e, r, found) {
			f.index.drop(leaseID)
		}
		return "", false, refuse(ErrLeaseUnknown, nil, "lease %q", leaseID)
	}
	return e.repoID, e.repoID != "", nil
}

// liveFor reports whether r is the held or committed reservation the entry was
// granted for, on the repo the caller named. Anything else is not a live lease.
func liveFor(e leaseEntry, r economics.Reservation, found bool) bool {
	live := r.State == economics.ReservationHeld || r.State == economics.ReservationCommitted
	return found && live && r.ID == e.reservationID && r.RepoID == e.repoID
}

// retired reports whether the reservation the entry was granted for can never
// serve it again: gone, replaced, or in a terminal state. A parked reservation
// is not retired (Unpark returns it to held); it answers as unknown meanwhile.
func retired(e leaseEntry, r economics.Reservation, found bool) bool {
	terminal := r.State == economics.ReservationReleased || r.State == economics.ReservationRolledBack
	return !found || terminal || r.ID != e.reservationID || r.RepoID != e.repoID
}

// pruneIndex drops the retired entries. A read error keeps the entry:
// RepoOfLease re-checks the reservation on use.
func (f *Fleet) pruneIndex(ctx context.Context) {
	for id, e := range f.index.all() {
		if r, found, err := f.deps.Reservations.GetByExecution(ctx, e.executionID); err == nil && retired(e, r, found) {
			f.index.drop(id)
		}
	}
}
