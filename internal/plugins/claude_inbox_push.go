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
// Outputs: records + claude.AppendResult{SessionID, Appended}; the sticky
// session taint (sessions.MarkUntrusted) committed before any untrusted
// record leaves the queue; typed errors to the dispatch loop, which
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
	"errors"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/sessions"
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
	taint     *sessions.Store // marks sessions outside any drain transaction
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
	return &ClaudeInboxBridge{
		push: push, hydration: hydration, attention: attention, clock: clock,
		taint: sessions.New(store, clock, nil),
	}, nil
}

// ReadNextTurnForSession drains sessionID's pending records for its next
// turn. The daemon's context.slice adapter is the production caller. No
// untrusted record leaves the queue unless the drain transaction itself reads
// the session as marked: a first pass that finds untrusted records on an
// unmarked session aborts, MarkUntrusted(session, first untrusted record id)
// commits, and the drain runs again. An unknown session or a mark error
// returns nothing and leaves the queue unchanged.
func (b *ClaudeInboxBridge) ReadNextTurnForSession(ctx context.Context, sessionID string) ([]claude.InboxRecord, claude.AppendResult, error) {
	none := claude.AppendResult{SessionID: sessionID}
	recs, res, err := b.hydration.AppendNextTurnWith(ctx, sessionID, requireTaint(b.clock, sessionID))
	var need *taintNeeded
	if !errors.As(err, &need) {
		return recs, res, err
	}
	if err := b.taint.MarkUntrusted(ctx, sessionID, need.ref); err != nil {
		return nil, none, err
	}
	recs, res, err = b.hydration.AppendNextTurnWith(ctx, sessionID, requireTaint(b.clock, sessionID))
	if errors.As(err, &need) {
		return nil, none, errTaintNotCommitted
	}
	return recs, res, err
}

// requireTaint is the drain's beforeCommit. Inside the drain transaction it
// reads sessionID's taint (an unknown session aborts the drain) and aborts
// with taintNeeded when an untrusted record would reach an unmarked session.
// It never writes there: the sessions store's writers hold a process lock
// while they wait for the store, and the drain transaction holds the store.
func requireTaint(clock runtime.Clock, sessionID string) func(context.Context, []claude.InboxRecord) error {
	return func(ctx context.Context, recs []claude.InboxRecord) error {
		if len(recs) == 0 {
			return nil
		}
		st, err := sessions.New(drainReader{tx: claude.DrainTx(ctx)}, clock, nil).Taint(ctx, sessionID)
		if err != nil || st.Tainted {
			return err
		}
		for _, r := range recs {
			if r.Untrusted() {
				return &taintNeeded{ref: r.ID}
			}
		}
		return nil
	}
}

// taintNeeded aborts a drain whose untrusted records need the session marked
// first; ref is the first untrusted record's id.
type taintNeeded struct{ ref string }

func (e *taintNeeded) Error() string { return "plugins: session taint required before drain" }

var (
	errTaintNotCommitted = cascade.New(cascade.KindInternal, "plugins: session taint did not commit; drain refused")
	errDrainReadOnly     = cascade.New(cascade.KindInternal, "plugins: the drain's session view is read-only")
)

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
// never the subscriber's id. DataClass and Trust are the values notify
// stamped at ingestion (provenance.go is their only writer), copied as they
// are: this adapter applies no visibility floor of its own, and Source is
// never consulted. An unstamped Notification carries the zero values,
// restricted and untrusted, so a gap fails closed.
func inboxEventFrom(n notify.Notification) claude.InboxEvent {
	return claude.InboxEvent{
		ID: n.ID, Class: string(n.Class.Resolve()), OriginScope: n.OriginScope, TargetScope: n.TargetScope,
		TargetSession: n.TargetSession, TargetTask: n.TargetTask, Visibility: string(n.Visibility.Resolve()),
		CorrelationID: n.CorrelationID, ExpiresAt: n.ExpiresAt, Body: n.Payload,
		DataClass: n.DataClass, Trust: n.Trust,
	}
}

// drainReader presents the drain transaction as a read-only provider.Store,
// so a sessions.Store reads inside it. A nil tx (no drain in progress) and
// every write fail closed.
type drainReader struct{ tx provider.Tx }

func (s drainReader) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if s.tx == nil {
		return nil, errDrainReadOnly
	}
	return s.tx.Get(ctx, ns, key)
}

func (drainReader) Put(context.Context, string, string, []byte) error { return errDrainReadOnly }

func (drainReader) Delete(context.Context, string, string) error { return errDrainReadOnly }

func (drainReader) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, errDrainReadOnly
}

func (drainReader) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return errDrainReadOnly
}
