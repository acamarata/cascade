package daemon

// Purpose: T0-daemon-composition-root — registers fleet.attention.list/
//   get/ack (P1-E18-W4-S39-T1) on the daemon's RPC router over a real
//   internal/fleet/supervision.Store, and starts its fleet.sessions.changed
//   subscription, mirroring journal_rpc.go's RegisterFleetJournalHandler
//   precedent exactly: the composition logic (constructing a real Store
//   from the already-open provider.Store/clock/bus) lives in this
//   package, cmd/cascade only calls it with what buildRPCServer already
//   has open.
// Inputs: the daemon's shared *rpc.Registry, the already-open
//   provider.Store (cmd/cascade/daemon_unix_store.go's openRuntimeStore),
//   runtime.Clock, and the daemon's real *events.Bus.
// Outputs: fleet.attention.list/get/ack bound to a real
//   supervision.Store over supervision.DefaultNamespace, plus a running
//   Subscription goroutine that pushes items on a session's
//   blocked/stalled transition.
// Constraints: no graphStore is threaded in here (R-21.157(a) visibility
//   then collapses to own_scope only, per supervision.ResolveVisibleScopes's
//   documented nil-store contract) — wiring a real *scope.GraphStore
//   composition is a separate, larger change than this registration and
//   is left for a follow-up, exactly as recall_index.go's dbPath-based
//   second-connection pattern was for its own subsystem. A nil store
//   disables the namespace entirely, matching every sibling
//   registerXHandler's documented nil-store degradation.
//
// CONTRACT DEVIATION (subscription lifecycle, recorded, not papered
// over). supervision.NewSubscription's Run needs a ctx canceled at
// daemon shutdown to stop cleanly. buildRPCServer (this function's only
// call site) does not currently accept one — it takes bus/clock/
// logger/settings/paths/memoryAdmin/store only, and NINE call sites
// (the production one plus eight cmd/cascade integration tests, none in
// this ticket's files_scope) construct it. Adding a ctx parameter here
// would touch every one of those call sites, which is a real, separate
// change this ticket does not make. This function therefore registers
// the RPC handlers only; starting the subscription goroutine is left to
// whichever change threads a daemon-lifetime ctx through buildRPCServer
// — recorded as a testonly-allow.json exemption for
// supervision.NewSubscription, naming this file as the eventual call
// site once that ctx exists.
// SPORT: internal/daemon (ADD, T0-daemon-composition-root; attention
//   half of P1-E18-W4-S39-T1). See internal/build/testonly-allow.json's
//   supervision.NewSubscription entry for the recorded ctx-threading gap
//   this file's RegisterFleetAttentionHandler deliberately does not
//   close.

import (
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// RegisterFleetAttentionHandler mounts fleet.attention.list/get/ack on
// registry over a real supervision.Store built from store/clock/bus. A
// nil store registers nothing, matching every sibling registerXHandler's
// documented nil-store degradation.
func RegisterFleetAttentionHandler(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus) {
	if store == nil {
		return
	}
	var eventBus supervision.EventBus
	if bus != nil {
		eventBus = bus
	}
	attnStore := supervision.NewStore(store, clock, eventBus, supervision.NewSystemIDGenerator(), 0)
	supervision.RegisterHandlers(registry, attnStore, nil)
}
