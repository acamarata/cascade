// Purpose (this file): the fleet.quota.snapshot JSON-RPC 2.0 method.
//
//	Mirrors internal/fleet/capacity/rpc.go's precedent: a generic
//	*rpc.Registry.Register call (registry.go needs no method-specific
//	change -- see this file's CONTRACT DEVIATION note), and the
//	composition-root wiring lives in internal/daemon (this file's
//	RegisterHandlers is called from internal/daemon/quota_rpc.go).
//
// Inputs: a *QuotaStore and the domains to report on, both threaded
//
//	through from the composition root that owns the real cascade.db
//	connection.
//
// Outputs: the QuotaSnapshot result, or a typed error when the store is
//
//	not yet initialised.
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over). The
// ticket names internal/rpc/registry.go in files_scope.change.
// Registry.Register is already a generic, method-name-agnostic call (see
// internal/fleet/capacity/rpc.go's identical CONTRACT DEVIATION note,
// itself following internal/fleet/sessions and internal/conversation) --
// registry.go needs no edit for this method to exist.
//
// SPORT: fleet.quota.rpc (ADD, per T3 sport_updates).

package topology

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodFleetQuotaSnapshot is the fleet.quota.snapshot JSON-RPC 2.0 method
// name.
const MethodFleetQuotaSnapshot = "fleet.quota.snapshot"

// DomainSource supplies the current QuotaDomain list a fleet.quota.snapshot
// call reports on. Duck-typed so this package never depends on a concrete
// domain-listing implementation (matches capacity.Compositor's own seam
// pattern one file over).
type DomainSource interface {
	ListQuotaDomains(ctx context.Context) ([]QuotaDomain, error)
}

// Handler returns the fleet.quota.snapshot rpc.HandlerFunc bound to store,
// domains and clock. A nil store returns a typed KindUnavailable error
// (the daemon-not-ready path), matching capacity.Handler's identical
// precedent, rather than panicking or returning an empty-but-confirmed
// snapshot.
func Handler(store *QuotaStore, domains DomainSource, clock Clock) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		if store == nil || domains == nil || clock == nil {
			return nil, cascade.New(cascade.KindUnavailable, "fleet.quota.snapshot: daemon quota subsystem not yet initialised")
		}
		if err := ctx.Err(); err != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, err, "fleet.quota.snapshot: context canceled")
		}
		domainList, err := domains.ListQuotaDomains(ctx)
		if err != nil {
			return nil, err
		}
		return TakeQuotaSnapshot(ctx, store, domainList, clock.Now())
	}
}

// RegisterHandlers binds MethodFleetQuotaSnapshot on reg. See this file's
// CONTRACT DEVIATION note for why registry.go itself needs no change.
func RegisterHandlers(reg *rpc.Registry, store *QuotaStore, domains DomainSource, clock Clock) {
	reg.Register(MethodFleetQuotaSnapshot, Handler(store, domains, clock))
}
