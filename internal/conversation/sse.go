package conversation

// Purpose (this file): the conversation.turn_appended SSE mirror --
//   CLIENT-LOCAL ECHO's wire half. Whenever AppendTurn commits, adapter.go
//   calls emitTurnAppended (below) before returning the JSON-RPC response:
//   the event is substituted through the H/S-16.T1 egress firewall and
//   published on the D/S-06.T4 events.Bus under the "conversation"
//   namespace, exactly the shape internal/fleet/sessions/domain.go's
//   Store.emit uses for "fleet.sessions.changed" (EventBus interface,
//   namespace/kind constants, best-effort-vs-blocking is the one
//   deliberate divergence: sessions' emit is fire-and-forget observability
//   and swallows Publish errors; CLIENT-LOCAL ECHO is a structural
//   invariant of THIS ticket, so emitTurnAppended returns ErrSSEWriteFailed
//   on a Publish failure rather than swallowing it, and adapter.go's
//   handleAppendTurn propagates that error to the caller instead of
//   reporting success for a turn no SSE consumer will ever see echoed.
// Inputs: a committed Turn plus its Segments, an EventBus, and a
//   Substitutor (egress substitution).
// Outputs: the published events.Event, or ErrEgressSubstitutionFailed /
//   ErrSSEWriteFailed.
// Constraints: PLATFORM. Windows tier-2 (06-FORGE-SPEC §2) has no daemon
//   and therefore no events.Bus at all -- RefuseSSEOnEmbedded is the
//   parameter-driven, portable refusal (unit-testable on every platform,
//   matching internal/fleet/resume/resume.go's RefuseOnGOOS precedent and
//   its own doc comment on why this logic must NOT live in a
//   "_windows.go"-suffixed file). It is NOT a second copy of an existing
//   daemon-layer refusal: internal/fleet/sessions/sse.go's ServeHTTP
//   documents that ITS handler adds no platform check because the
//   composition root already never starts a daemon on Windows at all; this
//   package's refusal is a DIFFERENT thing -- adapter.go's AppendTurn path
//   runs in-process even in the daemonless one-shot embedded mode (there is
//   no HTTP handler to refuse to mount), so the mode check has to live at
//   the call site that would otherwise try to Publish with no bus. See
//   platform_windows.go for windows's own thin entry point over this same
//   portable function.
// SPORT: internal.conversation.sse/ADDED (P1-E20-W5-S43-T2).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// turnAppendedNamespace and turnAppendedKind are this ticket's single
// SSE topic: namespace "conversation", event "conversation.turn_appended"
// -- mirroring R-21.272's ratified fleet.sessions single-namespace shape.
const (
	turnAppendedNamespace                  = "conversation"
	turnAppendedKind      events.EventKind = "conversation.turn_appended"
)

// ModeEmbedded names the Windows tier-2 daemonless one-shot mode
// (06-FORGE-SPEC §2): chat.append_turn still writes to storage and
// returns success, but no SSE mirror is attempted at all.
const ModeEmbedded = "embedded"

// EventBus is the minimal seam emitTurnAppended publishes through,
// duck-typed against *events.Bus's own Publish signature -- matching
// internal/fleet/sessions.EventBus's identical precedent so this package
// never requires importing a concrete Bus construction path in tests.
type EventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// Substitutor is the H/S-16.T1 egress substitution seam: it must return
// content with every vaulted value replaced by a typed reference tag, or
// a non-nil error and a nil result -- fail closed, never a partially
// substituted payload (internal/hooks/egress.SubstitutionPass's own
// documented contract; this interface exists so this package depends on
// a two-method shape it owns, not on importing internal/hooks/egress's
// concrete Vault/Detector/Rewriter wiring, which is the daemon
// composition root's job).
type Substitutor interface {
	Substitute(ctx context.Context, content []byte) ([]byte, error)
}

// turnAppendedPayload is the wire shape of one conversation.turn_appended
// SSE event: the full turn plus its segments, exactly as task 3 of this
// ticket requires ("the event carries the full turn payload").
type turnAppendedPayload struct {
	Turn     Turn      `json:"turn"`
	Segments []Segment `json:"segments"`
}

// RefuseSSEOnEmbedded reports ErrSSEUnavailableOnEmbedded when mode is
// ModeEmbedded, nil otherwise. Kept as a portable, parameter-driven
// function (not a "_windows.go"-suffixed file) so it compiles and is
// unit-tested on every platform -- see this file's own doc comment and
// internal/fleet/resume/resume.go's RefuseOnGOOS, whose exact reasoning
// this mirrors.
func RefuseSSEOnEmbedded(mode string) error {
	if mode == ModeEmbedded {
		return ErrSSEUnavailableOnEmbedded
	}
	return nil
}

// emitTurnAppended is CLIENT-LOCAL ECHO's producer half: it marshals
// turn+segments, substitutes the payload through subst, and publishes it
// on bus under turnAppendedNamespace/turnAppendedKind. Called by
// adapter.go's handleAppendTurn AFTER the store commit and BEFORE the
// JSON-RPC response is returned to the caller -- the ordering the
// invariant requires. mode == ModeEmbedded short-circuits to (zero,
// ErrSSEUnavailableOnEmbedded) without touching bus or subst at all,
// which is what lets a caller running the embedded one-shot path (no bus,
// no substitutor even constructed) hit this function safely.
//
// A consumer that stops reading does not block this call: events.Bus's
// Publish only appends to the namespace's durable log and wakes waiting
// subscribers (bus.go) -- it never blocks on a slow or absent reader, so
// a producer can never stall behind a stuck SSE client. Bounding a single
// connection's own backlog is internal/fleet/sessions/sse.go's
// sseSubscribeBuffer concern (64-deep buffered channel); this package
// introduces no new subscriber-side loop and therefore no new
// backpressure surface.
func emitTurnAppended(ctx context.Context, bus EventBus, subst Substitutor, mode string, turn Turn, segments []Segment) (events.Event, error) {
	if err := RefuseSSEOnEmbedded(mode); err != nil {
		return events.Event{}, err
	}
	if bus == nil {
		return events.Event{}, cascade.New(cascade.KindUnavailable, "conversation: no event bus configured for the SSE mirror")
	}
	raw, err := json.Marshal(turnAppendedPayload{Turn: turn, Segments: segments})
	if err != nil {
		return events.Event{}, cascade.Wrap(cascade.KindInternal, err, "conversation: marshal turn_appended payload")
	}
	substituted := raw
	if subst != nil {
		substituted, err = subst.Substitute(ctx, raw)
		if err != nil {
			// Never interpolate the substitutor's own error text: it ran
			// over conversation content and its message could echo a
			// fragment back (this file's PRIVACY constraint, matching
			// errors.go's static-literal-only rule).
			return events.Event{}, ErrEgressSubstitutionFailed
		}
	}
	ev, err := bus.Publish(ctx, turnAppendedNamespace, turnAppendedKind, "conversation:"+turn.ThreadID, substituted)
	if err != nil {
		return events.Event{}, ErrSSEWriteFailed
	}
	return ev, nil
}
