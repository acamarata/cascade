package daemon

// Purpose: T0-daemon-composition-root — registers fleet.sessions.list
//   (P1-E12-W6-S121-T1, closing P1-E12-W3-S24-T3's UNOWNED testonly-allow
//   gap) on the daemon's RPC router over a real
//   internal/fleet/sessions.Store, mirroring journal_rpc.go's
//   RegisterFleetJournalHandler precedent exactly: the composition logic
//   (build the domain Store, register its handlers) lives in this
//   package; cmd/cascade only calls it with the store/clock/bus it
//   already has open.
// Inputs: the daemon's shared *rpc.Registry, the already-open
//   provider.Store, the runtime.Clock, and the *events.Bus
//   buildRPCServer already threads into every sibling registerXHandler.
// Outputs: fleet.sessions.list bound to a real sessions.Store over
//   sessions.DefaultNamespace.
// Constraints: a nil store disables the namespace entirely
//   (registerRecallHandler/RegisterFleetJournalHandler's existing
//   nil-store degradation), rather than constructing a store over a
//   store that does not exist. bus is passed through as a typed nil
//   check, not a direct interface conversion: assigning a nil
//   *events.Bus straight into sessions.New's EventBus interface
//   parameter would produce a non-nil interface wrapping a nil pointer
//   (Store.emit's own "s.bus == nil" guard would then be false and the
//   first Upsert would call Publish on a nil receiver) — this file
//   converts only when bus is genuinely non-nil, so Store.emit's
//   documented nil-bus best-effort degradation stays reachable exactly
//   as its own doc comment describes.
// SPORT: internal/daemon (ADD, P1-E12-W6-S121-T1).

import (
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// RegisterFleetSessionsHandler mounts fleet.sessions.list on registry
// over a real sessions.Store built from store/clock/bus. A nil store
// registers nothing, matching RegisterFleetJournalHandler's documented
// degradation.
func RegisterFleetSessionsHandler(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus) {
	if store == nil {
		return
	}
	var eventBus sessions.EventBus
	if bus != nil {
		eventBus = bus
	}
	sessionStore := sessions.New(store, clock, eventBus)
	sessions.RegisterHandlers(registry, sessionStore)
}
