package supervision

// Purpose (this file): the C/S-04.T3 event-bus subscription (ticket task
// 5): consumes the SAME fleet.sessions.changed stream
// internal/fleet/sessions.Store.emit already publishes on every
// successful Put (domain.go), and calls Push for every event whose
// decoded SessionRecord.State reads as sessions.IsAttentionState
// (Blocked or Stalled).
//
// CONTRACT DEVIATION (event shape, recorded, not papered over). The
// ticket's HOW step 4 reads as if internal/fleet/sessions/statemachine.go
// itself must publish a NEW, distinct event kind on a blocked/stalled
// transition. statemachine.go's Advance is documented pure (no
// concurrency, no wall-clock read, no side effect — see its header
// comment, unchanged by this ticket) and has no bus dependency; no
// production caller in this tree currently persists Advance's output
// through sessions.Store.Put either (grep for NewStateMachine/.Advance(
// found only two call sites, cmd/cascade/fleet.go's embedded one-shot
// path and internal/nodes/heartbeat.go, neither of which writes through
// sessions.Store) — so there is no real "transition" moment to hook a
// NEW event kind onto yet, only Store.emit's EXISTING
// fleet.sessions.changed publish on every write. This file therefore
// subscribes to that real, already-wired stream and inspects the
// payload's State field directly (sessions.IsAttentionState, the new
// pure predicate statemachine.go now exports), rather than inventing a
// second event kind with no producer. This satisfies the acceptance
// criterion literally ("a session-state-machine blocked or stalled event
// results in a new AttentionItem... within one event-bus delivery
// cycle") against the real wiring: whichever ticket eventually adds the
// daemon-side poller that calls Advance and persists its result through
// sessions.Store.Put will cause this subscription to fire, with zero
// further change needed here.
//
// Inputs: an events.Bus (or any SubscriberBus) and this package's Store.
// Outputs: none directly; AttentionItems appear in store as a side
// effect.
//
// SPORT: fleet.supervision.Subscription/ADDED (P1-E18-W4-S39-T1).

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
)

// sessionsChangedNamespace and sessionsChangedKind mirror
// internal/fleet/sessions/domain.go's own unexported constants of the
// same value (that package's changedNamespace/changedKind are
// unexported, so this file restates the wire value rather than importing
// an internal identifier that does not exist for external packages).
const (
	sessionsChangedNamespace                  = "fleet.sessions"
	sessionsChangedKind      events.EventKind = "fleet.sessions.changed"
)

// subscriptionCursorName is this subscriber's durable cursor name on the
// fleet.sessions namespace (Bus.Subscribe's cursorName argument).
const subscriptionCursorName = "supervision:attention"

// subscriptionBuffer bounds this subscription's delivery backlog.
const subscriptionBuffer = 64

// SubscriberBus is the minimal seam Run needs, duck-typed against
// *events.Bus's own Subscribe signature.
type SubscriberBus interface {
	Subscribe(ctx context.Context, namespace, cursorName string, bufferSize int) (*events.Subscription, error)
}

// Subscription drives one long-lived consumer of the fleet.sessions
// stream. The zero value is not usable; construct with NewSubscription.
type Subscription struct {
	store *Store
	alive atomic.Bool
}

// NewSubscription builds a Subscription that pushes into store.
func NewSubscription(store *Store) *Subscription {
	return &Subscription{store: store}
}

// Alive reports whether Run's delivery loop is currently subscribed —
// doctorcheck.go's liveness probe.
func (s *Subscription) Alive() bool {
	return s.alive.Load()
}

// Run subscribes to bus's fleet.sessions.changed stream and processes
// events until ctx is done, the subscription's Events channel closes, or
// its Errs channel signals. It returns nil on a clean stop (ctx
// cancellation is the expected shutdown path for a daemon-lifetime
// subscription) and the Subscribe error if the initial subscribe fails.
func (s *Subscription) Run(ctx context.Context, bus SubscriberBus) error {
	sub, err := bus.Subscribe(ctx, sessionsChangedNamespace, subscriptionCursorName, subscriptionBuffer)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	s.alive.Store(true)
	defer s.alive.Store(false)
	return s.loop(ctx, sub)
}

// loop is Run's delivery loop, split out so Run's own defers (Unsubscribe,
// alive=false) always run once loop returns.
func (s *Subscription) loop(ctx context.Context, sub *events.Subscription) error {
	for {
		select {
		case ev, open := <-sub.Events:
			if !open {
				return nil
			}
			s.handle(ctx, ev)
		case <-sub.Errs:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// handle decodes ev as a sessions.SessionRecord and, if its State is an
// attention-eligible state (sessions.IsAttentionState), pushes an item.
// A malformed payload or a Push error is swallowed (best-effort
// observability, mirroring internal/fleet/sessions.PublishLaneHealth's
// identical fire-and-forget rationale) rather than aborting the whole
// subscription over one bad event.
func (s *Subscription) handle(ctx context.Context, ev events.Event) {
	if ev.Kind != sessionsChangedKind {
		return
	}
	var rec sessions.SessionRecord
	if err := json.Unmarshal(ev.Payload, &rec); err != nil {
		return
	}
	state, ok := sessions.ParseSessionState(rec.State)
	if !ok || !sessions.IsAttentionState(state) {
		return
	}
	item := AttentionItem{
		Kind:      kindForSessionState(state),
		SourceRef: rec.SessionID,
		ScopeRef:  ScopeRef{Kind: scope.ScopeKindSession, ID: rec.SessionID},
	}
	_, _ = s.store.Push(ctx, item)
}

// kindForSessionState maps a session state to the AttentionItem Kind it
// escalates as: Stalled maps to KindStall (R/S-39.T5's own stall
// detector's name), and Blocked maps to KindPolicyAsk — a blocked
// session, in this tree, is one waiting on something it cannot resolve
// itself, which is exactly what "policy-ask" names (as opposed to
// "elevation-refused", which R/S-39.T2's ceiling-refused push already
// covers explicitly, or "error", which is a hard failure this state
// machine transition is not). Documented here since sessions.SessionState
// carries no Kind mapping of its own — this package owns the choice.
func kindForSessionState(state sessions.SessionState) Kind {
	if state == sessions.StateStalled {
		return KindStall
	}
	return KindPolicyAsk
}
