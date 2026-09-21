package plugins

// Purpose (this file): the CLIENT-LOCAL ECHO half of install.EventBus --
//   every install-flow event rendered as a real conversation turn over the
//   SAME chat.append_turn transport cascadepa_wiring.go's cascadePAClient
//   already uses for `cascade chat` (internal/conversation.MethodAppendTurn,
//   the S-43.T2 adapter's registered wire method), plus multiEventPublisher,
//   which combines that echo with the durable internal/events.Bus record
//   cascadepa_install_events.go already provides.
//
// REWORK (round-1 adversarial CR B4): the prior draft published ONLY to
//   internal/events.Bus, a separate log the ticket's full_desc never
//   names, and disclosed that an already-connected SSE listener would not
//   see it live. The ticket's own text is explicit: "emit it via the
//   S-43.T2 conversation adapter as a typed conversation turn... The emit
//   follows the CLIENT-LOCAL ECHO invariant." This file is that adapter
//   call, reusing cascadepa_wiring.go's own appendTurnParams/
//   appendSegmentWire wire shapes and rpcDoer seam -- no new transport
//   code, no new wire format.
//
// Inputs: install.Event values Flow (plugins/cascade-pa/install/flow.go)
//   already builds.
// Outputs: one chat.append_turn RPC call per event (role "system", so it
//   renders as a host-authored notice rather than putting words in the
//   operator's own mouth), plus one durable events.Bus.Publish call.
// Constraints: every rendered string is built from the event's own typed
//   fields (PluginID, Version, Reason, Kind) only -- never a raw error
//   message -- matching install/events.go's own VALUE-FREE payload
//   constraint. Both halves are always attempted (errors.Join): a durable-
//   bus failure must never suppress the echo attempt, and vice versa.
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// chatEchoPublisher emits an install.Event as a real conversation turn.
type chatEchoPublisher struct {
	client *cascadePAClient
}

// newChatEchoPublisher builds a chatEchoPublisher over its own
// cascadePAClient, dialing the daemon's real unix socket -- the identical
// construction cascadepa_wiring.go's init() uses for `cascade chat`, so
// this seam carries no second transport implementation.
func newChatEchoPublisher(resolvePaths pathResolver) *chatEchoPublisher {
	return &chatEchoPublisher{client: newCascadePAClient(client.UnixDialer, cascadePAClientTimeout, resolvePaths)}
}

// Publish sends evt as one system-role turn on evt.ThreadID.
//
// round-2 rework (T0 decision D4): an evt with no ThreadID gets NO echo --
// leaving ThreadID empty in appendTurnParams would mint a fresh
// conversation thread per event (internal/conversation/privacy.go's
// resolveAppendThread), scattering an install run's own events across a
// new thread each, never the operator's own conversation. A typed log
// line replaces the echo in that case; the durable bus record
// (multiEventPublisher's other half) is unaffected.
func (p *chatEchoPublisher) Publish(ctx context.Context, evt install.Event) error {
	if evt.ThreadID == "" {
		slog.Default().Warn("cascade-pa install: echo skipped, no originating conversation thread",
			"event_kind", string(evt.Kind))
		return nil
	}
	rpc, err := p.client.rpcClient()
	if err != nil {
		return err
	}
	params := appendTurnParams{
		ThreadID: evt.ThreadID,
		Role:     "system",
		Segments: []appendSegmentWire{{Kind: "text", Content: renderInstallEventText(evt)}},
	}
	var result appendTurnResult
	return rpc.Do(ctx, conversation.MethodAppendTurn, params, &result)
}

// renderInstallEventText builds evt's human-readable echo text from its
// typed fields only.
func renderInstallEventText(evt install.Event) string {
	switch evt.Kind {
	case install.EventInstallProposal:
		if evt.Proposal == nil {
			return "cascade-pa install: proposing a plugin install"
		}
		p := evt.Proposal
		return "cascade-pa install: propose installing " + p.Name + " (" + p.PluginID + ") v" + p.Version +
			" for intent \"" + p.Intent + "\" -- reply to confirm"
	case install.EventInstallDeclined:
		if evt.Declined == nil {
			return "cascade-pa install: declined"
		}
		return "cascade-pa install: declined installing " + evt.Declined.PluginID +
			" (" + string(evt.Declined.Reason) + ")"
	case install.EventInstallFailed:
		if evt.Failed == nil {
			return "cascade-pa install: failed"
		}
		return "cascade-pa install: failed to install " + evt.Failed.PluginID +
			" (" + evt.Failed.Reason.String() + ")"
	case install.EventElevationRequired:
		if evt.Elevation == nil {
			return "cascade-pa install: elevation required"
		}
		return "cascade-pa install: " + evt.Elevation.PluginID + " needs local elevation approval before it can install"
	case install.EventConversationResume:
		if evt.Resume == nil {
			return "cascade-pa install: resuming"
		}
		if evt.Resume.AlreadyInstalled {
			return "cascade-pa install: " + evt.Resume.PluginID + " was already installed -- resuming \"" + evt.Resume.Intent + "\""
		}
		return "cascade-pa install: " + evt.Resume.PluginID + " installed -- resuming \"" + evt.Resume.Intent + "\""
	default:
		return "cascade-pa install: " + string(evt.Kind)
	}
}

// multiEventPublisher implements install.EventBus by delivering evt through
// BOTH the real conversation echo and the durable bus record. Both are
// always attempted; a failure on either is reported (errors.Join), never
// silently swallowed.
type multiEventPublisher struct {
	echo    *chatEchoPublisher
	durable *busEventPublisher
}

// Publish implements install.EventBus.
func (m *multiEventPublisher) Publish(ctx context.Context, evt install.Event) error {
	echoErr := m.echo.Publish(ctx, evt)
	durableErr := m.durable.Publish(ctx, evt)
	return errors.Join(echoErr, durableErr)
}

// compile-time proof multiEventPublisher really is the EventBus
// install.Flow reads.
var _ install.EventBus = (*multiEventPublisher)(nil)
