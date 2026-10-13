// Package plugins (this file): the cascade-claude inbox host adapter
// (P1-E37-W8-S73-T1).
//
// Purpose: bridge W/S-49.T1's notify.Registry deliveries into
// plugins/claude's InboxPush (direct push or durable next-turn hydration),
// raise a global-critical event on the daemon's attention queue through
// supervision.RoutePush, and hand a session's pending records back at its
// next UserPromptSubmit pass (ReadNextTurnForSession, called by the
// daemon's context.slice adapter cmd/cascade/daemon_unix_inbox_hydration.go).
//
// Inputs: NewClaudeInboxBridge(ctx, store, clock, attention) — the daemon's
// provider.Store, its clock and its attention store; WireClaudeInboxPush
// subscribes one active session's CandidateSession.
// Outputs: records + claude.AppendResult{SessionID, Appended} (the taint
// signal P1-E37-W8-S150-T1 marks); typed errors to the dispatch loop, which
// re-delivers on the next drain.
//
// Constraints: plugins/claude never imports internal/** (this file is the
// bridge). No package globals, no init. Delivery-time filtering applies the
// router's own predicates — notify.ScopeDeliveryPredicate and notify.Visible
// — never a re-derivation. data_class is derived from immutable provenance
// only; notify.Notification.Source (a caller-supplied tag) is never read.
// No path acknowledges the source inbox record (S-73.T2).
//
// SPORT: internal/plugins cascade-claude inbox host adapter (ADD) —
// P1-E37-W8-S73-T1.
package plugins

import (
	"context"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/claude"
)

// claudeInboxSubscriberPrefix prefixes each session subscriber's id.
const claudeInboxSubscriberPrefix = "cascade-claude-inbox:"

// ErrInboxSessionClosed refuses a delivery that raced the session's
// unsubscribe; the notification stays in the source inbox.
var ErrInboxSessionClosed = cascade.New(cascade.KindConflict, "plugins: cascade-claude inbox session already closed")

// ClaudeInboxBridge is the constructed adapter. The zero value is unusable.
type ClaudeInboxBridge struct {
	push      *claude.InboxPush
	hydration *claude.InboxHydration
	attention *supervision.Store
	clock     runtime.Clock
}

// NewClaudeInboxBridge builds the hydration queue over store, the channel
// and the coordinator, and negotiates the channel once (a deferral leaves
// the push path off; hydration is unaffected). All inputs are required.
func NewClaudeInboxBridge(ctx context.Context, store provider.Store, clock runtime.Clock, attention *supervision.Store) (*ClaudeInboxBridge, error) {
	if store == nil || clock == nil || attention == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "plugins: NewClaudeInboxBridge requires a store, a clock and an attention store")
	}
	hydration, err := claude.NewInboxHydration(store, clock.Now)
	if err != nil {
		return nil, err
	}
	push, err := claude.NewInboxPush(claude.NewCCChannel(), hydration)
	if err != nil {
		return nil, err
	}
	if err := push.Start(ctx); err != nil {
		return nil, err
	}
	return &ClaudeInboxBridge{push: push, hydration: hydration, attention: attention, clock: clock}, nil
}

// ReadNextTurnForSession drains sessionID's pending records for its next
// turn. The daemon's context.slice adapter is the production caller.
func (b *ClaudeInboxBridge) ReadNextTurnForSession(ctx context.Context, sessionID string) ([]claude.InboxRecord, claude.AppendResult, error) {
	return b.hydration.AppendNextTurn(ctx, sessionID)
}

// WireClaudeInboxPush subscribes session to reg for addressed delivery. It
// never widens the session's scope: the CandidateSession is the caller's.
// Deliveries run under ctx; the returned func unsubscribes, after which a
// racing delivery is refused with ErrInboxSessionClosed.
func WireClaudeInboxPush(ctx context.Context, b *ClaudeInboxBridge, reg *notify.Registry, session notify.CandidateSession) (func(), error) {
	if b == nil || reg == nil || session.SessionID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "plugins: WireClaudeInboxPush requires a bridge, a registry and a session id")
	}
	sub := &claudeInboxSubscriber{ctx: ctx, bridge: b, session: session}
	reg.Subscribe(sub, session)
	return func() {
		sub.closed.Store(true)
		reg.Unsubscribe(sub.ID())
	}, nil
}

// claudeInboxSubscriber is one active session's notify.Subscriber.
type claudeInboxSubscriber struct {
	ctx     context.Context
	bridge  *ClaudeInboxBridge
	session notify.CandidateSession
	closed  atomic.Bool
}

func (s *claudeInboxSubscriber) ID() string { return claudeInboxSubscriberPrefix + s.session.SessionID }

// Match defers every narrowing to receive, which applies the router's own
// predicates at delivery time.
func (s *claudeInboxSubscriber) Match(notify.Notification) bool { return true }

// Receive hands n to the bridge under the subscription's context.
func (s *claudeInboxSubscriber) Receive(n notify.Notification) error {
	return s.bridge.receive(s.ctx, s, n)
}

// receive is one delivery: refuse a closed or canceled subscription, drop
// an expired item, raise global-critical on the attention queue, withhold
// anything the router's predicates or R-16.60 visibility refuse for this
// session, then route. A permanent refusal (permanentRouteError) is withheld
// rather than returned, so the dispatch loop does not retry it forever; the
// item stays readable through the source inbox.
func (b *ClaudeInboxBridge) receive(ctx context.Context, s *claudeInboxSubscriber, n notify.Notification) error {
	if s.closed.Load() {
		return ErrInboxSessionClosed
	}
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "plugins: cascade-claude inbox delivery canceled")
	}
	now := b.clock.Now()
	if !n.ExpiresAt.IsZero() && !now.Before(n.ExpiresAt) {
		return nil
	}
	if n.Class.Resolve() == notify.ClassGlobalCritical {
		if err := b.raiseAttention(ctx, n); err != nil {
			return err
		}
	}
	if !notify.ScopeDeliveryPredicate(s.session, n) || !notify.Visible(s.session, n) {
		return nil
	}
	_, err := b.push.Route(ctx, s.session.SessionID, inboxEventFrom(n))
	if permanentRouteError(err) {
		return nil
	}
	return err
}

// permanentRouteError reports an error retrying cannot cure: a refused
// envelope, or a session queue this plugin found full or unreadable.
// Returned to the dispatcher it would be re-delivered to every subscriber on
// each drain, so a session that already hydrated the item would receive it
// again; the item stays readable through the source inbox instead. Any other
// store failure, whatever its Kind, is returned for redelivery.
func permanentRouteError(err error) bool {
	return cascade.HasKind(err, cascade.KindInvalidInput) || claude.PermanentHydrationError(err)
}

// raiseAttention files a global-critical item on the daemon's attention
// queue through RoutePush (same-scope global push: no cross-scope grant is
// involved; the item carries only the inbox id, never content). Pushes are
// idempotent per (kind, source_ref), so every session's subscriber may
// call it and exactly one item results.
func (b *ClaudeInboxBridge) raiseAttention(ctx context.Context, n notify.Notification) error {
	global := supervision.ScopeRef{Kind: scope.ScopeKindGlobal}
	_, err := supervision.RoutePush(ctx, b.attention, nil, nil, supervision.PushRequest{
		Item:   supervision.AttentionItem{Kind: supervision.KindError, SourceRef: "inbox:" + n.ID, ScopeRef: global},
		Origin: global,
	})
	return err
}

// inboxEventFrom builds the plugin DTO. TargetSession is n's own field,
// never the subscriber's id. DataClass stays the zero value, restricted: a
// Notification carries no data class and deriving one is P1-HRN-03's work,
// so the class is the fail-closed default and Source is never consulted.
// Trust is untrusted: inbox text is data.
func inboxEventFrom(n notify.Notification) claude.InboxEvent {
	return claude.InboxEvent{
		ID: n.ID, Class: string(n.Class.Resolve()), OriginScope: n.OriginScope, TargetScope: n.TargetScope,
		TargetSession: n.TargetSession, TargetTask: n.TargetTask, Visibility: string(n.Visibility.Resolve()),
		CorrelationID: n.CorrelationID, ExpiresAt: n.ExpiresAt, Body: n.Payload,
		DataClass: provider.SensitivityRestricted, Trust: provider.ProvenanceUntrustedSource,
	}
}
