// Purpose: the functional lease and worktree double behind the Reserver seams.
// It holds real state: a glob held by a live lease refuses a second acquirer
// and names the holder, a release must name live leases, and a worktree must
// belong to a live lease, so tests can prove which resources were acquired and
// that the teardown released exactly those, in reverse order.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// leaseBook is the in-memory resource ledger of the seams.
type leaseBook struct {
	mu        sync.Mutex
	seq       int
	holder    map[string]string // glob -> live lease id
	live      map[string]bool
	worktrees map[string]string // worktree id -> lease id
	released  []string          // every lease id released, in release order
	removed   []string          // every worktree id removed, in removal order
}

func newLeaseBook() *leaseBook {
	return &leaseBook{holder: map[string]string{}, live: map[string]bool{}, worktrees: map[string]string{}}
}

// acquire leases every glob or none: a held glob refuses with the holder's id.
func (b *leaseBook) acquire(globs []string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range globs {
		if id, held := b.holder[g]; held {
			return nil, cascade.Newf(cascade.KindConflict, "glob %q is held by lease %q", g, id)
		}
	}
	ids := make([]string, 0, len(globs))
	for _, g := range globs {
		b.seq++
		id := fmt.Sprintf("lease-%d", b.seq)
		b.holder[g], b.live[id] = id, true
		ids = append(ids, id)
	}
	return ids, nil
}

// release frees live leases; an id that is not live is an error.
func (b *leaseBook) release(ids []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if !b.live[id] {
			return cascade.Newf(cascade.KindNotFound, "lease %q is not held", id)
		}
		delete(b.live, id)
		for g, h := range b.holder {
			if h == id {
				delete(b.holder, g)
			}
		}
		b.released = append(b.released, id)
	}
	return nil
}

// allocate binds a worktree to a live lease.
func (b *leaseBook) allocate(leaseID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live[leaseID] {
		return "", cascade.Newf(cascade.KindNotFound, "lease %q is not held", leaseID)
	}
	id := "wt-" + leaseID
	b.worktrees[id] = leaseID
	return id, nil
}

// remove deletes a worktree; it must exist.
func (b *leaseBook) remove(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.worktrees[id]; !ok {
		return cascade.Newf(cascade.KindNotFound, "worktree %q does not exist", id)
	}
	delete(b.worktrees, id)
	b.removed = append(b.removed, id)
	return nil
}

// liveCount is the number of leases still held.
func (b *leaseBook) liveCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.live)
}

// worktreeCount is the number of worktrees still allocated.
func (b *leaseBook) worktreeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.worktrees)
}

// releasedIDs is a copy of the release order.
func (b *leaseBook) releasedIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.released)
}

// seams wires the Reserver to the lease book, recording each call by name.
func (f *fx) seams() economics.ReserverSeams {
	buckets := func(context.Context, string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error) {
		out := map[string]topology.Bucket{}
		for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
			out[dim] = topology.Bucket{Name: dim, Limit: 1_000_000, LimitScopeID: "scope-1", CapacityObserved: 1_000_000}
		}
		return topology.QuotaDomainAPIProject, "scope-1", out, nil
	}
	return economics.ReserverSeams{
		Buckets: buckets, Projects: &economics.ActiveProjectCount{},
		ReserveFraction: func(context.Context, string) (float64, error) { return 0, nil },
		Permit: func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
			return governor.Permit{}, nil
		},
		AcquireLeases: func(ctx context.Context, _, _ string, globs []string) ([]string, error) {
			f.calls.add("acquire_leases")
			if f.onAcquire != nil {
				f.onAcquire(ctx)
			}
			return f.book.acquire(globs)
		},
		ReleaseLeases: func(_ context.Context, ids []string) error {
			f.calls.add("release_leases")
			if f.releaseErr != nil {
				return f.releaseErr
			}
			return f.book.release(ids)
		},
		AllocateWorktree: func(_ context.Context, leaseID string) (string, error) {
			f.calls.add("allocate_worktree")
			return f.book.allocate(leaseID)
		},
		RemoveWorktree: func(_ context.Context, id string) error {
			f.calls.add("remove_worktree")
			if f.removeErr != nil {
				return f.removeErr
			}
			return f.book.remove(id)
		},
		ValidateLeases: func(_ context.Context, _ string, ids []string) ([]string, error) { return ids, nil },
		RenewLeases:    func(context.Context, []string) error { return nil },
		FindLeases:     func(context.Context, string, string, []string) ([]string, error) { return nil, nil },
		FindWorktree:   func(context.Context, string) (string, bool, error) { return "", false, nil },
		RaiseAttention: func(context.Context, string, string) error { return nil },
	}
}
