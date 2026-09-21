package plugins

// Purpose (this file): the real install.EventBus the composition root
//   (cascadepa_install_wiring.go) injects via install.SetEventBus -- the
//   seam events.go's own doc comment says "stands in for S-43.T2's
//   conversation adapter". This file adapts the daemon's real
//   internal/events.Bus onto that seam, the identical technique
//   cascadepa_bridge_events.go already uses for the Telegram bridge's own
//   journal (BridgeEventPublisher/bridgeJournal): a narrow Publisher
//   interface *events.Bus satisfies structurally, so a test drives this
//   file with an in-memory double and never a real provider.Store.
// Inputs: values install/events.go's Flow already builds (install.Event).
// Outputs: one events.Bus.Publish call per install-flow phase transition,
//   under the "cascade-pa.install" namespace -- a durable, queryable
//   record any consumer (a future `cascade pa install-log`, a support
//   bundle, `cascade context slice`) can replay, per
//   internal/events/types.go's documented open EventKind convention (the
//   same reasoning cascadepa_bridge_events.go's header already gives for
//   choosing this bus over internal/audit's closed Kind enum).
// Constraints:
//   - DISCLOSED LIMITATION, not papered over: this composition root opens
//     its OWN *events.Bus instance against the daemon's cascade.db (see
//     cascadepa_install_wiring.go's dbEventBus lazy constructor), the same
//     "own second sqlite connection" tradeoff wirePluginAddHandler,
//     chat_wiring.go and context_cmd.go already carry for this exact file
//     (cmd/cascade is out of this ticket's files_scope, so this
//     composition root cannot be handed the daemon's single live *Bus
//     the way wireCascadePABridge is). Every publish is durably persisted
//     through the SHARED cascade.db a caller can Replay -- what it is NOT
//     is a live wakeup for a goroutine already blocked in another *Bus
//     instance's Subscribe (bus_subscribe.go's per-instance wake channel
//     lives in memory, not in the store), so an already-connected SSE
//     listener sees a cross-instance publish on its NEXT natural poll,
//     not instantly. Recorded here rather than assumed away.
//   - Flow.publish (install/flow.go) treats a non-nil Publish error as a
//     reason to abort the run -- unlike bridgeJournal's own discard-on-
//     failure design (a different risk tradeoff for a different caller),
//     this adapter returns the bus's error unchanged.
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// installEventNamespace is the bus namespace every install-flow event is
// recorded under.
const installEventNamespace = "cascade-pa.install"

// installEventSource identifies the publisher on the bus.
const installEventSource = "cascade-pa/install"

// installEventPublisher is the narrow seam this file needs from
// *events.Bus, declared here (not imported as a concrete type) so a test
// drives publishInstallEvent with an in-memory double -- the identical
// pattern cascadepa_bridge_events.go's BridgeEventPublisher already
// establishes for this same package.
type installEventPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind,
		source string, payload []byte) (events.Event, error)
}

// busEventPublisher adapts an installEventPublisher onto install.EventBus.
type busEventPublisher struct {
	bus installEventPublisher
}

// newBusEventPublisher builds a busEventPublisher over bus. A nil bus is
// refused BY Publish (fail-closed, matching install.EventBus's own
// unconfigured default's contract), not tolerated here.
func newBusEventPublisher(bus installEventPublisher) *busEventPublisher {
	return &busEventPublisher{bus: bus}
}

// Publish implements install.EventBus by JSON-encoding evt whole (its
// fields are already VALUE-FREE per install/events.go's own header) and
// recording it on the bus. install.EventKind and events.EventKind are
// both plain string types with matching literal values by construction
// (install/events.go's five consts), so no translation table is needed --
// only cascadepa_bridge_events.go's own EventKindBridge* consts need one,
// because bridge events use its OWN kind vocabulary, not install's.
func (p *busEventPublisher) Publish(ctx context.Context, evt install.Event) error {
	if p == nil || p.bus == nil {
		return cascade.New(cascade.KindUnavailable,
			"cascade-pa install: no event bus wired into this session; the composition root "+
				"has not called cascadepa_install_wiring's dbEventBus")
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade-pa install: encode event")
	}
	_, err = p.bus.Publish(ctx, installEventNamespace, events.EventKind(evt.Kind), installEventSource, payload)
	return err
}

// compile-time proof busEventPublisher really is the EventBus install.Flow
// reads, so a signature drift on either side fails here rather than at the
// wiring site.
var _ install.EventBus = (*busEventPublisher)(nil)
