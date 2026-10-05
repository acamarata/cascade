// Purpose: Add's pool-membership rules, moved out of core.go so it stays
//   under the 300-line limit: the prior-record lookup, the keep-or-refuse
//   pool decision for a re-add, and the max+1 join index read through
//   Registry.ListPool.
// Inputs: the Registry seam, the provider name and the request's --pool.
// Outputs: the pool and index the upserted record carries, or a typed
//   refusal.
// Constraints: works on every Registry implementation (no concrete-type
//   assertion). A lookup error other than not-found refuses Add instead of
//   guessing membership: a guess would upsert a second, bare-name lane.
// SPORT: provider.intake/ADD.

package intake

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// priorRecord returns the record already registered under name and whether
// one exists. KindNotFound means a first add; any other error refuses Add,
// because without the prior record a re-add cannot keep its membership.
func priorRecord(ctx context.Context, reg Registry, name string) (ProviderRecord, bool, error) {
	rec, err := reg.GetProvider(ctx, name)
	switch {
	case err == nil:
		return rec, true, nil
	case cascade.HasKind(err, cascade.KindNotFound):
		return ProviderRecord{}, false, nil
	default:
		return ProviderRecord{}, false, err
	}
}

// membershipFor decides the pool and index the upserted record carries. A
// re-add without --pool keeps the pool the provider already holds. A re-add
// naming any other pool (including a standalone provider joining one) is a
// KindConflict: the lane is keyed by pool, so moving would leave the old
// lane behind as a second one.
func membershipFor(ctx context.Context, reg Registry, name, requested string, prior ProviderRecord, found bool) (string, int, error) {
	pool := requested
	if found {
		if requested != "" && requested != prior.Pool {
			return "", 0, poolConflict(name, prior.Pool, requested)
		}
		pool = prior.Pool
	}
	if pool == "" {
		return "", 0, nil
	}
	idx, err := poolJoinIndex(ctx, reg, pool, name)
	if err != nil {
		return "", 0, err
	}
	return pool, idx, nil
}

// poolConflict is the typed refusal for a re-add that names a pool other
// than the one name already holds.
func poolConflict(name, held, requested string) error {
	if held == "" {
		return cascade.Newf(cascade.KindConflict,
			"intake: provider %q is standalone; remove it before adding it to pool %q", name, requested)
	}
	return cascade.Newf(cascade.KindConflict,
		"intake: provider %q is in pool %q; re-add without --pool to keep it, or remove it before adding it to pool %q",
		name, held, requested)
}

// poolJoinIndex returns name's round-robin index in pool. An existing
// member keeps its own index; a new one gets one past the highest index
// any member holds (0 for an empty pool). A ListPool error refuses the
// join rather than defaulting to an index another member may already hold.
func poolJoinIndex(ctx context.Context, reg Registry, pool, name string) (int, error) {
	members, err := reg.ListPool(ctx, pool)
	if err != nil {
		return 0, err
	}
	next := 0
	for _, m := range members {
		if m.Name == name {
			return m.PoolIndex, nil
		}
		if m.PoolIndex >= next {
			next = m.PoolIndex + 1
		}
	}
	return next, nil
}
